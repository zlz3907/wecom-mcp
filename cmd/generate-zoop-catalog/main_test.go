package main

import "testing"

func TestConvertStripsTenantIDsAndMarksFormulaUnsupported(t *testing.T) {
	multiple := false
	source := sourceMirror{Version: 1, Roles: map[string]struct {
		Fields []sourceField `json:"fields"`
	}{}}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields := []sourceField{{Title: primaryFieldTitles[role], Type: "FIELD_TYPE_TEXT"}}
		if role == "Z-S01" {
			fields = append(fields,
				sourceField{Title: "阶段", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"待确认": "secret-option-id"}},
				sourceField{Title: "项目", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "CRHIKw", ReferenceTargetFieldID: "secret-field-id", ReferenceIsMultiple: &multiple},
				sourceField{Title: "进度", Type: "FIELD_TYPE_FORMULA"},
			)
		}
		source.Roles[role] = struct {
			Fields []sourceField `json:"fields"`
		}{Fields: fields}
	}
	got, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if got.CompleteForCreation || len(got.UnsupportedForCreate) != 1 {
		t.Fatalf("formula boundary was not preserved: %#v", got)
	}
	var reference *logicalReference
	for _, field := range got.Roles[0].Fields {
		if field.Reference != nil {
			reference = field.Reference
		}
	}
	if reference == nil || reference.Role != "Z-S07" {
		t.Fatalf("tenant reference was not translated: %#v", got.Roles[0].Fields)
	}
}
