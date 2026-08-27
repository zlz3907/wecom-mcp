package zoopschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedCatalogIsTenantNeutralAndCompleteAsObserved(t *testing.T) {
	catalog, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"Z-S01": 42, "Z-S02": 17, "Z-S03": 22, "Z-S04": 10, "Z-S05": 11, "Z-S06": 14, "Z-S07": 8, "Z-S08": 7, "Z-S09": 16}
	wantTitles := map[string]string{"Z-S01": "Z-S01｜主需求表", "Z-S02": "Z-S02｜决策表", "Z-S03": "Z-S03｜任务表", "Z-S04": "Z-S04｜审计表", "Z-S05": "Z-S05｜计划任务表", "Z-S06": "Z-S06｜会话表", "Z-S07": "Z-S07｜项目表", "Z-S08": "Z-S08｜Schema 契约表", "Z-S09": "Z-S09｜协作主体表"}
	for role, want := range wantCounts {
		if got := catalog.FieldCounts()[role]; got != want {
			t.Fatalf("%s field count=%d want=%d", role, got, want)
		}
	}
	for _, role := range catalog.Roles {
		if role.SheetTitle != wantTitles[role.Role] {
			t.Fatalf("%s title=%q want=%q", role.Role, role.SheetTitle, wantTitles[role.Role])
		}
	}
	if catalog.CompleteForCreation {
		t.Fatal("formula field must keep create-time catalog fail-closed")
	}
	if len(catalog.UnsupportedForCreate) != 1 || catalog.UnsupportedForCreate[0] != "Z-S01.进度条" {
		t.Fatalf("unexpected unsupported fields: %#v", catalog.UnsupportedForCreate)
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"field_id", "sheet_id", "docid", "option_id", "f04Gwj", "q979lj", "o09ow6"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("tenant-specific identifier leaked into embedded catalog: %s", forbidden)
		}
	}
}

func TestLogicalReferencesContainNoTenantIDs(t *testing.T) {
	catalog, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, role := range catalog.Roles {
		for _, field := range role.Fields {
			if field.Reference == nil {
				continue
			}
			found = true
			if !strings.HasPrefix(field.Reference.Role, "Z-S0") || field.Reference.FieldTitle == "" {
				t.Fatalf("invalid logical reference: %#v", field.Reference)
			}
		}
	}
	if !found {
		t.Fatal("expected logical references")
	}
}

func TestLogicalReferenceTopologyMatchesNormativeModel(t *testing.T) {
	catalog, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Reference{
		"Z-S01.所属项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称"},
		"Z-S01.需求提出主体": {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S02.受影响任务":  {Role: "Z-S03", FieldTitle: "任务编号", Multiple: true},
		"Z-S02.决策主体":   {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S02.主需求":    {Role: "Z-S01", FieldTitle: "需求编号"},
		"Z-S03.任务执行主体": {Role: "Z-S09", FieldTitle: "主体编号", Multiple: true},
		"Z-S03.任务责任主体": {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S03.主需求":    {Role: "Z-S01", FieldTitle: "需求编号"},
		"Z-S04.操作主体":   {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S05.调度主体":   {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S06.所属项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称"},
		"Z-S06.执行主体":   {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S06.发起主体":   {Role: "Z-S09", FieldTitle: "主体编号"},
		"Z-S07.参与主体":   {Role: "Z-S09", FieldTitle: "主体编号", Multiple: true},
		"Z-S09.参与项目":   {Role: "Z-S07", FieldTitle: "项目编号与名称", Multiple: true},
	}
	got := map[string]Reference{}
	for _, role := range catalog.Roles {
		for _, field := range role.Fields {
			if field.Reference != nil {
				got[role.Role+"."+field.Title] = *field.Reference
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("logical reference count=%d want=%d: %#v", len(got), len(want), got)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("%s=%#v want=%#v", key, got[key], expected)
		}
	}
}
