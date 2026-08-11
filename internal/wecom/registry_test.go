package wecom

import "testing"

func TestTargetsFromSheetListResolvesSeveralRoles(t *testing.T) {
	list := []any{
		map[string]any{"sheet_id": "requirement", "name": "Z-S01｜需求主项"},
		map[string]any{"sheet_id": "task", "name": "Z-S03｜执行任务"},
	}
	targets, err := targetsFromSheetList("document", []string{"Z-S01", "Z-S03"}, list)
	if err != nil {
		t.Fatal(err)
	}
	if targets["Z-S01"].SheetID != "requirement" || targets["Z-S03"].SheetID != "task" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestTargetsFromSheetListRejectsMissingRole(t *testing.T) {
	_, err := targetsFromSheetList("document", []string{"Z-S01", "Z-S03"}, []any{
		map[string]any{"sheet_id": "requirement", "name": "Z-S01｜需求主项"},
	})
	if err == nil {
		t.Fatal("missing role must fail closed")
	}
}
