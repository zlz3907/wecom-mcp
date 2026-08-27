package main

import "testing"

func TestConvertStripsTenantIDsAndMarksFormulaUnsupported(t *testing.T) {
	multiple := false
	source := sourceMirror{Version: 1, Roles: map[string]struct {
		Fields []sourceField `json:"fields"`
	}{}}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields := []sourceField{{Title: primaryFieldTitles[role], ID: "SYMBOLIC_PRIMARY_" + role, Type: "FIELD_TYPE_TEXT"}}
		if role == "Z-S01" {
			fields = append(fields,
				sourceField{Title: "阶段", ID: "SYMBOLIC_STAGE_FIELD", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"待确认": "SYMBOLIC_OPTION_TOKEN"}},
				sourceField{Title: "所属项目", ID: "SYMBOLIC_REFERENCE_FIELD", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "SYMBOLIC_SHEET_TOKEN", ReferenceTargetFieldID: "SYMBOLIC_PRIMARY_Z-S07", ReferenceIsMultiple: &multiple},
				sourceField{Title: "进度", ID: "SYMBOLIC_FORMULA_FIELD", Type: "FIELD_TYPE_FORMULA"},
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

func TestConvertAddsVerifiedLogicalProgressFormula(t *testing.T) {
	source := sourceMirror{Version: 1, Roles: map[string]struct {
		Fields []sourceField `json:"fields"`
	}{}}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields := []sourceField{{Title: primaryFieldTitles[role], ID: "SYMBOLIC_PRIMARY_" + role, Type: "FIELD_TYPE_TEXT"}}
		if role == "Z-S01" {
			fields = append(fields,
				sourceField{Title: "已完成任务数", ID: "SYMBOLIC_COMPLETED", Type: "FIELD_TYPE_NUMBER"},
				sourceField{Title: "当前任务总数", ID: "SYMBOLIC_TOTAL", Type: "FIELD_TYPE_NUMBER"},
				sourceField{Title: "进度条", ID: "SYMBOLIC_PROGRESS", Type: "FIELD_TYPE_FORMULA"},
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
		t.Fatalf("formula write capability was incorrectly unlocked: %#v", got)
	}
	foundFormula := false
	for _, field := range got.Roles[0].Fields {
		if field.Title == "进度条" && (field.Formula == nil || field.Formula.Model[0].FieldTitle != "已完成任务数") {
			t.Fatalf("logical formula missing: %#v", field)
		}
		if field.Title == "进度条" {
			foundFormula = true
		}
	}
	if !foundFormula {
		t.Fatal("verified progress formula was not generated")
	}
}

func TestConvertRejectsReferenceNotInLogicalTopology(t *testing.T) {
	source := sourceMirror{Version: 1, Roles: map[string]struct {
		Fields []sourceField `json:"fields"`
	}{}}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		source.Roles[role] = struct {
			Fields []sourceField `json:"fields"`
		}{Fields: []sourceField{{Title: primaryFieldTitles[role], ID: "SYMBOLIC_PRIMARY_" + role, Type: "FIELD_TYPE_TEXT"}}}
	}
	role := source.Roles["Z-S08"]
	role.Fields = append(role.Fields, sourceField{Title: "未授权关联", ID: "SYMBOLIC_UNMODELLED_FIELD", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "SYMBOLIC_SHEET_TOKEN"})
	source.Roles["Z-S08"] = role
	if _, err := convert(source); err == nil {
		t.Fatal("unmodelled reference must fail closed")
	}
}

func TestConvertRejectsMisdirectedProtectedReference(t *testing.T) {
	multiple := false
	source := sourceMirror{Version: 1, Roles: map[string]struct {
		Fields []sourceField `json:"fields"`
	}{}}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		source.Roles[role] = struct {
			Fields []sourceField `json:"fields"`
		}{Fields: []sourceField{{Title: primaryFieldTitles[role], ID: "SYMBOLIC_PRIMARY_" + role, Type: "FIELD_TYPE_TEXT"}}}
	}
	role := source.Roles["Z-S01"]
	role.Fields = append(role.Fields, sourceField{
		Title: "所属项目", ID: "SYMBOLIC_REFERENCE_FIELD", Type: "FIELD_TYPE_REFERENCE",
		ReferenceTargetSheetID: "SYMBOLIC_SHEET_TOKEN", ReferenceTargetFieldID: "SYMBOLIC_PRIMARY_Z-S09", ReferenceIsMultiple: &multiple,
	})
	source.Roles["Z-S01"] = role
	if _, err := convert(source); err == nil {
		t.Fatal("misdirected protected reference must fail closed")
	}
}
