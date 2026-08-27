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
	Role              string  `json:"role"`
	SheetTitle        string  `json:"sheet_title"`
	PrimaryFieldTitle string  `json:"primary_field_title"`
	Fields            []Field `json:"fields"`
}

type Field struct {
	Title                string     `json:"field_title"`
	Type                 string     `json:"field_type"`
	Options              []string   `json:"options,omitempty"`
	Reference            *Reference `json:"reference,omitempty"`
	Formula              *Formula   `json:"formula,omitempty"`
	UnsupportedForCreate bool       `json:"unsupported_for_create,omitempty"`
}

type Formula struct {
	Model     []FormulaToken   `json:"formula_model"`
	Formatter FormulaFormatter `json:"formatter"`
}

type FormulaToken struct {
	Type       string `json:"type"`
	FieldTitle string `json:"field_title,omitempty"`
	Text       string `json:"text,omitempty"`
}

type FormulaFormatter struct {
	Type          string `json:"type"`
	DecimalPlaces int    `json:"decimal_places"`
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
		if role.Role == "" || role.SheetTitle == "" || role.PrimaryFieldTitle == "" || len(role.Fields) == 0 {
			return Catalog{}, fmt.Errorf("embedded Zoop catalog role is incomplete")
		}
		if _, duplicate := seen[role.Role]; duplicate {
			return Catalog{}, fmt.Errorf("embedded Zoop catalog role is duplicated")
		}
		seen[role.Role] = struct{}{}
		primaryFound := false
		fieldTitles := map[string]struct{}{}
		for _, field := range role.Fields {
			if field.Title == "" || field.Type == "" {
				return Catalog{}, fmt.Errorf("embedded Zoop catalog field is incomplete for %s", role.Role)
			}
			fieldTitles[field.Title] = struct{}{}
			if field.Title == role.PrimaryFieldTitle && field.Type == "FIELD_TYPE_TEXT" {
				primaryFound = true
			}
		}
		for _, field := range role.Fields {
			if field.Type != "FIELD_TYPE_FORMULA" {
				continue
			}
			if field.Formula == nil || len(field.Formula.Model) == 0 || field.Formula.Formatter.Type == "" {
				return Catalog{}, fmt.Errorf("embedded Zoop catalog formula is incomplete for %s.%s", role.Role, field.Title)
			}
			for _, token := range field.Formula.Model {
				switch token.Type {
				case "FORMULA_TYPE_FIELD":
					if _, ok := fieldTitles[token.FieldTitle]; !ok || token.FieldTitle == field.Title {
						return Catalog{}, fmt.Errorf("embedded Zoop catalog formula dependency is invalid for %s.%s", role.Role, field.Title)
					}
				case "FORMULA_TYPE_TEXT":
					if token.Text == "" {
						return Catalog{}, fmt.Errorf("embedded Zoop catalog formula text is empty for %s.%s", role.Role, field.Title)
					}
				default:
					return Catalog{}, fmt.Errorf("embedded Zoop catalog formula token is invalid for %s.%s", role.Role, field.Title)
				}
			}
		}
		if !primaryFound {
			return Catalog{}, fmt.Errorf("embedded Zoop catalog primary field is invalid for %s", role.Role)
		}
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
