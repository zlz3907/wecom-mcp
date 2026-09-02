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
	ID                     string            `json:"field_id"`
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
	Role              string         `json:"role"`
	SheetTitle        string         `json:"sheet_title"`
	PrimaryFieldTitle string         `json:"primary_field_title"`
	Fields            []catalogField `json:"fields"`
}

type catalogField struct {
	Title                string            `json:"field_title"`
	Type                 string            `json:"field_type"`
	Options              []string          `json:"options,omitempty"`
	Reference            *logicalReference `json:"reference,omitempty"`
	Formula              *logicalFormula   `json:"formula,omitempty"`
	UnsupportedForCreate bool              `json:"unsupported_for_create,omitempty"`
}

type logicalFormula struct {
	Model     []logicalFormulaToken `json:"formula_model"`
	Formatter struct {
		Type          string `json:"type"`
		DecimalPlaces int    `json:"decimal_places"`
	} `json:"formatter"`
}

type logicalFormulaToken struct {
	Type       string `json:"type"`
	FieldTitle string `json:"field_title,omitempty"`
	Text       string `json:"text,omitempty"`
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

// referenceTopology is copied from the Owner-authorized relationship model as
// logical names only. Source tenant identifiers are used solely as an
// indication that the source field is a reference; they are never matched,
// embedded, logged, or copied into generated/runtime artifacts.
var referenceTopology = map[string]logicalReference{
	"Z-S01\x00所属项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称", Multiple: false},
	"Z-S01\x00需求提出主体": {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S02\x00主需求":    {Role: "Z-S01", FieldTitle: "需求编号", Multiple: false},
	"Z-S02\x00决策主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S02\x00受影响任务":  {Role: "Z-S03", FieldTitle: "任务编号", Multiple: true},
	"Z-S03\x00主需求":    {Role: "Z-S01", FieldTitle: "需求编号", Multiple: false},
	"Z-S03\x00任务执行主体": {Role: "Z-S09", FieldTitle: "主体编号", Multiple: true},
	"Z-S03\x00任务责任主体": {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S04\x00操作主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S05\x00调度主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S06\x00发起主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S06\x00所属项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称", Multiple: false},
	"Z-S06\x00执行主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: false},
	"Z-S07\x00参与主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: true},
	"Z-S09\x00参与项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称", Multiple: true},
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

func verifiedProgressFormula() *logicalFormula {
	formula := &logicalFormula{Model: []logicalFormulaToken{
		{Type: "FORMULA_TYPE_FIELD", FieldTitle: "已完成任务数"},
		{Type: "FORMULA_TYPE_TEXT", Text: "/"},
		{Type: "FORMULA_TYPE_FIELD", FieldTitle: "当前任务总数"},
	}}
	formula.Formatter.Type = "FIELD_TYPE_PROGRESS"
	formula.Formatter.DecimalPlaces = -1
	return formula
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
	fieldTargets := map[string]map[string]string{}
	for role, sourceRole := range source.Roles {
		fieldTargets[role] = map[string]string{}
		for _, field := range sourceRole.Fields {
			if field.ID == "" {
				return catalog{}, fmt.Errorf("%s.%s has no protected source field identifier", role, field.Title)
			}
			if _, duplicate := fieldTargets[role][field.ID]; duplicate {
				return catalog{}, fmt.Errorf("protected source field identifier is not unique within %s", role)
			}
			fieldTargets[role][field.ID] = field.Title
		}
	}
	sheetRoleClaims := map[string]string{}
	for index := 1; index <= 9; index++ {
		role := fmt.Sprintf("Z-S%02d", index)
		sourceRole, ok := source.Roles[role]
		if !ok || len(sourceRole.Fields) == 0 {
			return catalog{}, fmt.Errorf("missing %s", role)
		}
		out := catalogRole{Role: role, SheetTitle: sheetTitles[role], PrimaryFieldTitle: primaryFieldTitles[role]}
		primaryFound := false
		for _, field := range sourceRole.Fields {
			converted := catalogField{Title: field.Title, Type: field.Type}
			for label := range field.Options {
				converted.Options = append(converted.Options, label)
			}
			sort.Strings(converted.Options)
			expectedReference, topologyHasReference := referenceTopology[role+"\x00"+field.Title]
			sourceHasReference := field.ReferenceTargetSheetID != "" || field.ReferenceTargetFieldID != "" || field.ReferenceIsMultiple != nil
			if topologyHasReference != sourceHasReference {
				return catalog{}, fmt.Errorf("%s.%s reference presence differs from the logical relationship model", role, field.Title)
			}
			if topologyHasReference {
				if field.ReferenceTargetSheetID == "" || field.ReferenceTargetFieldID == "" || field.ReferenceIsMultiple == nil || *field.ReferenceIsMultiple != expectedReference.Multiple {
					return catalog{}, fmt.Errorf("%s.%s reference properties differ from the logical relationship model", role, field.Title)
				}
				targetTitle, found := fieldTargets[expectedReference.Role][field.ReferenceTargetFieldID]
				if !found || targetTitle != expectedReference.FieldTitle {
					return catalog{}, fmt.Errorf("%s.%s reference target differs from the logical relationship model", role, field.Title)
				}
				if claimedRole, claimed := sheetRoleClaims[field.ReferenceTargetSheetID]; claimed && claimedRole != expectedReference.Role {
					return catalog{}, fmt.Errorf("protected source sheet identifier maps to multiple roles")
				}
				sheetRoleClaims[field.ReferenceTargetSheetID] = expectedReference.Role
				reference := expectedReference
				converted.Reference = &reference
			}
			if field.Type == "FIELD_TYPE_FORMULA" {
				if role == "Z-S01" && field.Title == "进度条" {
					for _, dependencyTitle := range []string{"已完成任务数", "当前任务总数"} {
						found := false
						for _, candidate := range sourceRole.Fields {
							if candidate.Title == dependencyTitle {
								found = true
							}
						}
						if !found {
							return catalog{}, fmt.Errorf("%s.%s formula dependency %s is missing", role, field.Title, dependencyTitle)
						}
					}
					converted.Formula = verifiedProgressFormula()
					converted.UnsupportedForCreate = true
					result.CompleteForCreation = false
					result.UnsupportedForCreate = append(result.UnsupportedForCreate, role+"."+field.Title)
				} else {
					converted.UnsupportedForCreate = true
					result.CompleteForCreation = false
					result.UnsupportedForCreate = append(result.UnsupportedForCreate, role+"."+field.Title)
				}
			}
			if field.Title == out.PrimaryFieldTitle && field.Type == "FIELD_TYPE_TEXT" {
				primaryFound = true
			}
			out.Fields = append(out.Fields, converted)
		}
		if !primaryFound {
			return catalog{}, fmt.Errorf("%s primary field %s is missing or not text", role, out.PrimaryFieldTitle)
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
