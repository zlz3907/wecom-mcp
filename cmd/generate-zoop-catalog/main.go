// Command generate-zoop-catalog creates the tenant-neutral, create-time Zoop
// catalog from an Owner-authorized online schema mirror. It is an audit tool;
// the generated catalog is embedded at build time and this command is never
// used by the MCP runtime.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

type sourceMirror struct {
	Version int `json:"version"`
	Roles   map[string]struct {
		Fields []sourceField `json:"fields"`
	} `json:"roles"`
}

type sourceField struct {
	Title                  string            `json:"field_title"`
	Type                   string            `json:"field_type"`
	Options                map[string]string `json:"select_options"`
	ReferenceTargetSheetID string            `json:"reference_target_sheet_id"`
	ReferenceTargetFieldID string            `json:"reference_target_field_id"`
	ReferenceIsMultiple    *bool             `json:"reference_is_multiple"`
}

type catalog struct {
	Version              string        `json:"version"`
	SourceContract       string        `json:"source_contract"`
	CompleteForCreation  bool          `json:"complete_for_creation"`
	UnsupportedForCreate []string      `json:"unsupported_for_create,omitempty"`
	Roles                []catalogRole `json:"roles"`
}

type catalogRole struct {
	Role       string         `json:"role"`
	SheetTitle string         `json:"sheet_title"`
	Fields     []catalogField `json:"fields"`
}

type catalogField struct {
	Title                string            `json:"field_title"`
	Type                 string            `json:"field_type"`
	Options              []string          `json:"options,omitempty"`
	Reference            *logicalReference `json:"reference,omitempty"`
	UnsupportedForCreate bool              `json:"unsupported_for_create,omitempty"`
}

type logicalReference struct {
	Role       string `json:"role"`
	FieldTitle string `json:"field_title"`
	Multiple   bool   `json:"multiple"`
}

var sheetTitles = map[string]string{
	"Z-S01": "Z-S01｜主需求表",
	"Z-S02": "Z-S02｜决策表",
	"Z-S03": "Z-S03｜任务表",
	"Z-S04": "Z-S04｜审计表",
	"Z-S05": "Z-S05｜计划任务表",
	"Z-S06": "Z-S06｜会话表",
	"Z-S07": "Z-S07｜项目表",
	"Z-S08": "Z-S08｜Schema 契约表",
	"Z-S09": "Z-S09｜协作主体表",
}

// These IDs appear only in the one-time source mirror. They are translated to
// logical references and never copied to the generated catalog.
var sourceSheetRoles = map[string]string{
	"q979lj": "Z-S01",
	"lC40b5": "Z-S03",
	"CRHIKw": "Z-S07",
	"GiEyRl": "Z-S09",
}

var primaryFieldTitles = map[string]string{
	"Z-S01": "需求编号",
	"Z-S02": "决策标题",
	"Z-S03": "任务编号",
	"Z-S04": "审计事件编号",
	"Z-S05": "计划任务编号",
	"Z-S06": "会话编号与标题",
	"Z-S07": "项目编号与名称",
	"Z-S08": "Schema 条目键与版本",
	"Z-S09": "主体编号",
}

func main() {
	input := flag.String("input", "", "Owner-authorized online schema JSON mirror")
	output := flag.String("output", "", "tenant-neutral catalog output")
	flag.Parse()
	if *input == "" || *output == "" {
		fatalf("-input and -output are required")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		fatalf("read input: %v", err)
	}
	var source sourceMirror
	if err := json.Unmarshal(data, &source); err != nil || source.Version != 1 {
		fatalf("source mirror is not version 1")
	}
	generated, err := convert(source)
	if err != nil {
		fatalf("convert: %v", err)
	}
	encoded, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		fatalf("encode: %v", err)
	}
	if err := os.WriteFile(*output, append(encoded, '\n'), 0644); err != nil {
		fatalf("write output: %v", err)
	}
}

func convert(source sourceMirror) (catalog, error) {
	result := catalog{Version: "zoop-v1", SourceContract: "owner-authorized-online-schema-v1", CompleteForCreation: true}
	for index := 1; index <= 9; index++ {
		role := fmt.Sprintf("Z-S%02d", index)
		sourceRole, ok := source.Roles[role]
		if !ok || len(sourceRole.Fields) == 0 {
			return catalog{}, fmt.Errorf("missing %s", role)
		}
		out := catalogRole{Role: role, SheetTitle: sheetTitles[role]}
		for _, field := range sourceRole.Fields {
			converted := catalogField{Title: field.Title, Type: field.Type}
			for label := range field.Options {
				converted.Options = append(converted.Options, label)
			}
			sort.Strings(converted.Options)
			if field.ReferenceTargetSheetID != "" {
				targetRole := sourceSheetRoles[field.ReferenceTargetSheetID]
				if targetRole == "" {
					return catalog{}, fmt.Errorf("%s.%s has unknown reference target", role, field.Title)
				}
				multiple := false
				if field.ReferenceIsMultiple != nil {
					multiple = *field.ReferenceIsMultiple
				}
				converted.Reference = &logicalReference{Role: targetRole, FieldTitle: primaryFieldTitles[targetRole], Multiple: multiple}
			}
			if field.Type == "FIELD_TYPE_FORMULA" {
				converted.UnsupportedForCreate = true
				result.CompleteForCreation = false
				result.UnsupportedForCreate = append(result.UnsupportedForCreate, role+"."+field.Title)
			}
			out.Fields = append(out.Fields, converted)
		}
		sort.Slice(out.Fields, func(i, j int) bool { return out.Fields[i].Title < out.Fields[j].Title })
		result.Roles = append(result.Roles, out)
	}
	sort.Strings(result.UnsupportedForCreate)
	return result, nil
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
