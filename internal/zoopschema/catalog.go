package zoopschema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed catalog/zoop-v1.json
var zoopV1JSON []byte

type Catalog struct {
	Version              string   `json:"version"`
	SourceContract       string   `json:"source_contract"`
	CompleteForCreation  bool     `json:"complete_for_creation"`
	UnsupportedForCreate []string `json:"unsupported_for_create,omitempty"`
	Roles                []Role   `json:"roles"`
}

type Role struct {
	Role       string  `json:"role"`
	SheetTitle string  `json:"sheet_title"`
	Fields     []Field `json:"fields"`
}

type Field struct {
	Title                string     `json:"field_title"`
	Type                 string     `json:"field_type"`
	Options              []string   `json:"options,omitempty"`
	Reference            *Reference `json:"reference,omitempty"`
	UnsupportedForCreate bool       `json:"unsupported_for_create,omitempty"`
}

type Reference struct {
	Role       string `json:"role"`
	FieldTitle string `json:"field_title"`
	Multiple   bool   `json:"multiple"`
}

func Current() (Catalog, error) {
	var result Catalog
	if err := json.Unmarshal(zoopV1JSON, &result); err != nil {
		return Catalog{}, fmt.Errorf("embedded Zoop catalog is invalid: %w", err)
	}
	if result.Version != "zoop-v1" || len(result.Roles) != 9 {
		return Catalog{}, fmt.Errorf("embedded Zoop catalog is incomplete")
	}
	seen := map[string]struct{}{}
	for _, role := range result.Roles {
		if role.Role == "" || role.SheetTitle == "" || len(role.Fields) == 0 {
			return Catalog{}, fmt.Errorf("embedded Zoop catalog role is incomplete")
		}
		if _, duplicate := seen[role.Role]; duplicate {
			return Catalog{}, fmt.Errorf("embedded Zoop catalog role is duplicated")
		}
		seen[role.Role] = struct{}{}
	}
	return result, nil
}

func (c Catalog) RoleNames() []string {
	roles := make([]string, 0, len(c.Roles))
	for _, role := range c.Roles {
		roles = append(roles, role.Role)
	}
	sort.Strings(roles)
	return roles
}

func (c Catalog) FieldCounts() map[string]int {
	counts := make(map[string]int, len(c.Roles))
	for _, role := range c.Roles {
		counts[role.Role] = len(role.Fields)
	}
	return counts
}
