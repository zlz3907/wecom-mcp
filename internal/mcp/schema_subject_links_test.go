package mcp

import "testing"

func TestSubjectLinkMigrationCatalogHasStableExpectedContract(t *testing.T) {
	specs := subjectLinkMigrationSpecs()
	if len(specs) != 11 {
		t.Fatalf("subject link spec count=%d", len(specs))
	}
	if specs[0].Role != "Z-S01" || specs[0].Title != "需求提出主体" {
		t.Fatalf("unexpected first spec: %#v", specs[0])
	}
	if specs[len(specs)-1].Role != "Z-S07" || specs[len(specs)-1].Title != "参与主体" || !specs[len(specs)-1].IsMultiple {
		t.Fatalf("unexpected last spec: %#v", specs[len(specs)-1])
	}
}

func TestSubjectLinkMigrationFieldBuildsExactReferenceContract(t *testing.T) {
	wanted := subjectLinkMigrationField(subjectLinkSpec{
		Role:       "Z-S03",
		Title:      "任务执行主体",
		Type:       "FIELD_TYPE_REFERENCE",
		IsMultiple: true,
	}, "subject-sheet", "subject-field")
	property := wanted.Property["property_reference"].(map[string]any)
	if property["sub_id"] != "subject-sheet" || property["field_id"] != "subject-field" || property["is_multiple"] != true {
		t.Fatalf("unexpected reference property: %#v", property)
	}
	if err := compatibleSubjectLinkField("Z-S03", migrationFieldWire(wanted), wanted); err != nil {
		t.Fatal(err)
	}
}

func TestSubjectLinkCompatibilityFailsClosedOnDrift(t *testing.T) {
	wanted := subjectLinkMigrationField(subjectLinkSpec{
		Role:  "Z-S01",
		Title: "需求提出主体",
		Type:  "FIELD_TYPE_REFERENCE",
	}, "subject-sheet", "subject-field")

	wrongType := migrationFieldWire(wanted)
	wrongType["field_type"] = "FIELD_TYPE_TEXT"
	if err := compatibleSubjectLinkField("Z-S01", wrongType, wanted); err == nil {
		t.Fatal("type drift must fail")
	}

	wrongTarget := migrationFieldWire(wanted)
	wrongTarget["property_reference"] = map[string]any{"sub_id": "other", "field_id": "subject-field", "is_multiple": false}
	if err := compatibleSubjectLinkField("Z-S01", wrongTarget, wanted); err == nil {
		t.Fatal("reference target drift must fail")
	}

	wrongCardinality := migrationFieldWire(wanted)
	wrongCardinality["property_reference"] = map[string]any{"sub_id": "subject-sheet", "field_id": "subject-field", "is_multiple": true}
	if err := compatibleSubjectLinkField("Z-S01", wrongCardinality, wanted); err == nil {
		t.Fatal("reference cardinality drift must fail")
	}
}

func TestAssessSubjectLinkFieldsOnlyManagesRoleCatalog(t *testing.T) {
	missing, present, err := assessSubjectLinkFields("Z-S04", []map[string]any{
		{"field_id": "existing", "field_title": "既有业务字段", "field_type": "FIELD_TYPE_TEXT"},
	}, "subject-sheet", "subject-field")
	if err != nil {
		t.Fatal(err)
	}
	if present != 0 || len(missing) != 2 || missing[0].Title != "操作主体" || missing[1].Title != "来源会话ID" {
		t.Fatalf("unexpected assessment: present=%d missing=%#v", present, missing)
	}
}
