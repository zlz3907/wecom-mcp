package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type discoveryFake struct {
	t              *testing.T
	registry       []any
	schema         []any
	fields         []any
	requests       map[string]int
	incomplete     bool
	duplicateSheet bool
}

func (f *discoveryFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	f.t.Helper()
	if operation != "get_sheet" && operation != "get_fields" && operation != "get_records" {
		f.t.Fatalf("discovery attempted side effect: %s", operation)
	}
	input := payload.(map[string]any)
	doc := input["docid"].(string)
	if doc != "registry-document" && doc != "business-document" {
		f.t.Fatalf("discovery escaped fixed Registry: %q", doc)
	}
	f.requests[operation+":"+doc]++
	var result map[string]any
	switch operation {
	case "get_sheet":
		if doc == "registry-document" {
			items := []any{map[string]any{"type": "smartsheet", "sheet_id": "registry-sheet"}}
			if f.duplicateSheet {
				items = append(items, map[string]any{"type": "smartsheet", "sheet_id": "another-registry"})
			}
			result = map[string]any{"sheet_list": items}
		} else {
			result = map[string]any{"sheet_list": []any{
				map[string]any{"sheet_id": "requirement-sheet", "name": "Z-S01｜需求"},
				map[string]any{"sheet_id": "schema-sheet", "name": schemaRegistrySheetTitle},
			}}
		}
	case "get_fields":
		fields := f.fields
		if doc == "registry-document" {
			fields = []any{}
			for _, title := range []string{"registry_key", "docid", "lifecycle_status"} {
				fields = append(fields, map[string]any{"field_title": title, "field_id": title, "field_type": "FIELD_TYPE_TEXT"})
			}
		}
		result = map[string]any{"fields": fields}
	case "get_records":
		records := f.schema
		if doc == "registry-document" {
			records = f.registry
		}
		offset, limit := input["offset"].(int), input["limit"].(int)
		start := min(offset, len(records))
		end := start + min(limit, len(records)-start)
		result = map[string]any{"records": records[start:end], "has_more": end < len(records) || f.incomplete}
	}
	return map[string]any{"result": result}, nil
}

func discoveryFixture(t *testing.T) (config.Config, *discoveryFake, map[string]string) {
	t.Helper()
	runtime := config.Config{
		Version: 1, InstanceName: "binding-placeholder", TenantRoute: "source-test", RegistryDocumentID: "registry-document", RegistryKey: "registry-key",
		SchemaSource: "z-s00", StatePath: filepath.Join(t.TempDir(), "state.json"),
		APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}},
	}
	f := &discoveryFake{t: t, requests: map[string]int{}}
	cell := func(value string) []any { return []any{map[string]any{"text": value}} }
	f.registry = []any{map[string]any{"record_id": "registry-active", "values": map[string]any{
		"registry_key": cell(runtime.RegistryKey), "docid": cell("business-document"), "lifecycle_status": cell("active"),
	}}}
	ids := map[string]string{}
	for i, field := range schemaRegistryMigrationFields() {
		id := fmt.Sprintf("field-%d", i)
		ids[field.Title] = id
		f.fields = append(f.fields, map[string]any{"field_title": field.Title, "field_id": id, "field_type": "FIELD_TYPE_TEXT"})
	}
	onlineRuntime := runtime
	onlineRuntime.InstanceName = "existing-instance"
	snapshot := schemaRegistrySnapshot{CapturedAt: "2026-09-23T00:00:00Z", FieldCount: 9}
	for i := 1; i <= 9; i++ {
		role := fmt.Sprintf("Z-S%02d", i)
		snapshot.Entries = append(snapshot.Entries, map[string]string{
			"条目类型": "field", "实例名称": onlineRuntime.InstanceName, "Registry Key": runtime.RegistryKey,
			"表角色": role, "表名": role + "｜测试", "表 ID": fmt.Sprintf("sheet-%d", i), "字段名": "标题", "字段 ID": "title",
			"字段类型": "FIELD_TYPE_TEXT", "是否允许新增": "是", "是否允许更新": "是", "写入编码器": "text_cell_array", "Codec 验证状态": "已验证",
		})
	}
	var err error
	snapshot.Generation, err = schemaRegistryEntriesDigest(snapshot.Entries)
	if err != nil {
		t.Fatal(err)
	}
	f.schema = []any{map[string]any{"record_id": "active", "values": schemaRegistryPointerValues(snapshot, onlineRuntime, "revision", ids)}}
	for key, values := range schemaRegistryGenerationRecords(snapshot, onlineRuntime, "revision", ids) {
		f.schema = append(f.schema, map[string]any{"record_id": key, "values": values})
	}
	return runtime, f, ids
}

func TestDiscoveryResolvesExistingNameReadOnly(t *testing.T) {
	runtime, fake, _ := discoveryFixture(t)
	name, err := resolveDiscoveredInstanceName(context.Background(), runtime, fake)
	if err != nil || name != "existing-instance" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	for request, count := range fake.requests {
		if count != 1 {
			t.Fatalf("snapshot validation repeated read %s: %d", request, count)
		}
	}
	if runtime.InstanceName != "binding-placeholder" || runtime.AIExecutionSubjectRecordID != "" || runtime.WecomOperatorUserID != "" {
		t.Fatal("discovery changed caller identity")
	}
}

func TestDiscoveryFailsClosed(t *testing.T) {
	for _, scenario := range []string{"missing_active", "duplicate_active", "inactive", "invalid_name", "wrong_key", "incomplete_schema", "incomplete_page", "duplicate_registry_sheet", "late_duplicate_registry", "missing_permission", "duplicate_field"} {
		t.Run(scenario, func(t *testing.T) {
			runtime, fake, ids := discoveryFixture(t)
			active := fake.schema[0].(map[string]any)["values"].(map[string]any)
			cell := func(s string) []any { return []any{map[string]any{"text": s}} }
			switch scenario {
			case "missing_active":
				fake.schema = fake.schema[1:]
			case "duplicate_active":
				fake.schema = append(fake.schema, fake.schema[0])
			case "inactive":
				active[ids["生效状态"]] = cell("disabled")
			case "invalid_name":
				active[ids["实例名称"]] = cell("../other")
			case "wrong_key":
				active[ids["Registry Key"]] = cell("other-key")
			case "incomplete_schema":
				fake.schema = fake.schema[:1]
			case "incomplete_page":
				fake.incomplete = true
			case "duplicate_registry_sheet":
				fake.duplicateSheet = true
			case "late_duplicate_registry":
				for i := 0; i < 501; i++ {
					fake.registry = append(fake.registry, map[string]any{"values": map[string]any{"lifecycle_status": cell("inactive")}})
				}
				fake.registry = append(fake.registry, fake.registry[0])
			case "missing_permission":
				runtime.APIWhitelist = map[string][]string{"read": {"get_sheet", "get_fields"}}
			case "duplicate_field":
				fake.fields = append(fake.fields, fake.fields[0])
			}
			if name, err := resolveDiscoveredInstanceName(context.Background(), runtime, fake); err == nil || name != "" {
				t.Fatalf("invalid discovery accepted: name=%q err=%v", name, err)
			}
		})
	}
}
