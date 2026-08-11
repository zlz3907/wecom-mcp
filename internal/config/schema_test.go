package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.md")
	text := "# test\n"
	for index := 1; index <= 8; index++ {
		text += "## Z-S0" + string(rune('0'+index)) + "｜测试\n| 字段 | Field ID | 类型 | 属性 |\n| --- | --- | --- | --- |\n| 标题 | `field" + string(rune('0'+index)) + "` | `FIELD_TYPE_TEXT` | — |\n"
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles["Z-S01"]["标题"].ID != "field1" {
		t.Fatalf("unexpected schema: %#v", got)
	}
}

func TestWriteAndLoadOnlineMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	fields := map[string][]Field{}
	for index := 1; index <= 8; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields[role] = []Field{{Title: "标题", ID: "field" + string(rune('0'+index)), Type: "FIELD_TYPE_TEXT"}}
	}
	fields["Z-S01"] = append(fields["Z-S01"], Field{Title: "阶段", ID: "select1", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"待确认": "option1"}})
	isMultiple := false
	fields["Z-S01"] = append(fields["Z-S01"], Field{Title: "所属项目", ID: "reference1", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "project-sheet", ReferenceTargetFieldID: "project-title", ReferenceIsMultiple: &isMultiple})
	if err := WriteOnlineMirror(path, fields, "2026-08-09T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles["Z-S01"]["阶段"].Options["待确认"] != "option1" {
		t.Fatal("select option missing")
	}
	if got.Roles["Z-S01"]["所属项目"].ReferenceTargetSheetID != "project-sheet" {
		t.Fatal("reference target sheet missing")
	}
	if got.Roles["Z-S01"]["所属项目"].ReferenceTargetFieldID != "project-title" || got.Roles["Z-S01"]["所属项目"].ReferenceIsMultiple == nil || *got.Roles["Z-S01"]["所属项目"].ReferenceIsMultiple {
		t.Fatal("reference target field or cardinality missing")
	}
}

func TestLoadOnlineMirrorIncludesOptionalSubjectRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	fields := map[string][]Field{}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields[role] = []Field{{Title: "标题", ID: "field" + string(rune('0'+index)), Type: "FIELD_TYPE_TEXT"}}
	}
	if err := WriteOnlineMirror(path, fields, "2026-08-11T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Roles["Z-S09"]["标题"].ID != "field9" {
		t.Fatalf("Z-S09 missing from runtime schema: %#v", got.Roles["Z-S09"])
	}
}

func TestWriteReadableOnlineMirror(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.md")
	fields := map[string][]Field{}
	for index := 1; index <= 8; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields[role] = []Field{{Title: "标题", ID: "field" + string(rune('0'+index)), Type: "FIELD_TYPE_TEXT"}}
	}
	isMultiple := false
	fields["Z-S01"] = append(fields["Z-S01"], Field{Title: "阶段", ID: "select1", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"待确认": "option1"}})
	fields["Z-S01"] = append(fields["Z-S01"], Field{Title: "所属项目", ID: "reference1", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "project-sheet", ReferenceTargetFieldID: "project-title", ReferenceIsMultiple: &isMultiple})
	if err := WriteReadableOnlineMirror(path, fields, "2026-08-10T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"2026-08-10T00:00:00Z", "| Z-S01 | 3 |", "## Z-S01｜线上字段", "选项：待确认", "关联目标子表：`project-sheet`", "多选：false"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("readable mirror missing %q: %s", expected, text)
		}
	}
	loaded, err := LoadSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Roles["Z-S01"]["所属项目"].ID != "reference1" {
		t.Fatal("readable mirror cannot be inspected by the existing loader")
	}
}
