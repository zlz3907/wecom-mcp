package mcp

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestSchemaRegistryMigrationCatalogIsStableAndTextOnly(t *testing.T) {
	fields := schemaRegistryMigrationFields()
	if len(fields) != 28 {
		t.Fatalf("field count=%d, want 28", len(fields))
	}
	if fields[0].Title != "Schema 条目键" || fields[len(fields)-1].Title != "字段总数" {
		t.Fatalf("unexpected registry catalog boundaries: %#v", fields)
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if field.Type != "FIELD_TYPE_TEXT" {
			t.Fatalf("registry field %s type=%s, want text", field.Title, field.Type)
		}
		if seen[field.Title] {
			t.Fatalf("duplicate registry field %s", field.Title)
		}
		seen[field.Title] = true
	}
	for _, required := range []string{"表名", "表 ID", "字段名", "字段 ID", "字段类型", "写入编码器", "Schema Digest"} {
		if !seen[required] {
			t.Fatalf("missing required registry field %s", required)
		}
	}
}

func TestSchemaRegistryReservationResumesOnlySameRequest(t *testing.T) {
	server := New(filepath.Join(t.TempDir(), "unused-config.json"))
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := server.reserveSchemaRegistryWithOperator(statePath, "schema-registry-idempotency-1", "digest-1", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := server.reserveSchemaRegistryWithOperator(statePath, "schema-registry-idempotency-1", "digest-1", "owner"); err != nil {
		t.Fatalf("same pending request must resume: %v", err)
	}
	if err := server.reserveSchemaRegistryWithOperator(statePath, "schema-registry-idempotency-1", "digest-2", "owner"); err == nil {
		t.Fatal("same key with another digest must fail")
	}
	recovered, err := server.completeSchemaRegistryReservationIfPresent(statePath, "schema-registry-idempotency-1", "digest-1", "owner")
	if err != nil || !recovered {
		t.Fatalf("pending reservation was not recovered: recovered=%t err=%v", recovered, err)
	}
	recovered, err = server.completeSchemaRegistryReservationIfPresent(statePath, "schema-registry-idempotency-1", "digest-1", "owner")
	if err != nil || recovered {
		t.Fatalf("completed reservation must be stable: recovered=%t err=%v", recovered, err)
	}
}

func TestSchemaRegistryCodecSeparatesWritableAndSystemFields(t *testing.T) {
	for _, test := range []struct {
		fieldType string
		codec     string
		status    string
		writable  bool
	}{
		{"FIELD_TYPE_TEXT", "text_cell_array", "已验证", true},
		{"FIELD_TYPE_CHECKBOX", "boolean", "已验证", true},
		{"FIELD_TYPE_REFERENCE", "record_id_array", "已验证", true},
		{"FIELD_TYPE_AUTONUMBER", "system_read_only", "系统只读", false},
		{"FIELD_TYPE_ATTACHMENT", "unsupported", "公开写入契约不支持", false},
		{"FIELD_TYPE_FORMULA", "unsupported", "未验证", false},
	} {
		codec, status, writable := schemaRegistryCodec(test.fieldType)
		if codec != test.codec || status != test.status || writable != test.writable {
			t.Fatalf("%s => (%s,%s,%t), want (%s,%s,%t)", test.fieldType, codec, status, writable, test.codec, test.status, test.writable)
		}
	}
	if !schemaRegistrySystemField("FIELD_TYPE_FORMULA") || schemaRegistrySystemField("FIELD_TYPE_TEXT") {
		t.Fatal("system-field classification is inconsistent")
	}
}

func TestSchemaRegistryGenerationRequiresManifestAndAllFieldRows(t *testing.T) {
	fieldIDs := map[string]string{}
	for index, field := range schemaRegistryMigrationFields() {
		fieldIDs[field.Title] = "f" + strings.Repeat("x", index+1)
	}
	snapshot := schemaRegistrySnapshot{
		Generation: strings.Repeat("a", 64), CapturedAt: "2026-09-13T00:00:00Z", FieldCount: 9,
	}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + strconv.Itoa(index)
		snapshot.Entries = append(snapshot.Entries, map[string]string{
			"条目类型": "field", "表角色": role, "表名": role + "｜测试", "表 ID": "sheet-" + strconv.Itoa(index),
			"字段名": "字段", "字段 ID": "field-" + strconv.Itoa(index), "字段类型": "FIELD_TYPE_TEXT",
		})
	}
	var err error
	snapshot.Generation, err = schemaRegistryEntriesDigest(snapshot.Entries)
	if err != nil {
		t.Fatal(err)
	}
	expected := schemaRegistryGenerationRecords(snapshot, config.Config{InstanceName: "instance", RegistryKey: "registry"}, "revision", fieldIDs)
	if len(expected) != 10 {
		t.Fatalf("generation record count=%d, want manifest + 9 fields", len(expected))
	}
	missing, err := schemaRegistryMissingRecords(map[string]map[string]any{}, expected, fieldIDs)
	if err != nil || len(missing) != 10 {
		t.Fatalf("missing=%d err=%v", len(missing), err)
	}
	byKey := map[string]map[string]any{}
	for key, values := range expected {
		copied := map[string]any{}
		for fieldID, value := range values {
			copied[fieldID] = value
		}
		byKey[key] = map[string]any{"record_id": "record-" + key, "values": copied}
	}
	complete, count := schemaRegistryGenerationComplete(byKey, fieldIDs, snapshot.Generation)
	if !complete || count != 9 {
		t.Fatalf("complete=%t count=%d", complete, count)
	}
	manifestKey := "manifest:" + snapshot.Generation
	manifestValues := byKey[manifestKey]["values"].(map[string]any)
	manifestValues[fieldIDs["表名"]] = []any{map[string]any{"type": "text", "text": "手工篡改"}}
	if _, err := schemaRegistryMissingRecords(byKey, expected, fieldIDs); err == nil {
		t.Fatal("immutable generation accepted an unexpected non-empty field")
	}
	manifestValues[fieldIDs["表名"]] = nil
	fieldKey := snapshot.Generation + ":Z-S01:field-1"
	fieldValues := byKey[fieldKey]["values"].(map[string]any)
	originalFieldName := fieldValues[fieldIDs["字段名"]]
	fieldValues[fieldIDs["字段名"]] = []any{map[string]any{"type": "text", "text": "被篡改字段"}}
	if complete, _ := schemaRegistryGenerationComplete(byKey, fieldIDs, snapshot.Generation); complete {
		t.Fatal("generation digest did not detect a modified field row")
	}
	fieldValues[fieldIDs["字段名"]] = originalFieldName
	delete(byKey, snapshot.Generation+":Z-S02:field-2")
	complete, count = schemaRegistryGenerationComplete(byKey, fieldIDs, snapshot.Generation)
	if complete || count != 8 {
		t.Fatalf("incomplete generation accepted: complete=%t count=%d", complete, count)
	}
}

func TestSchemaRegistryUpdateSchemaHasCASAndNoCallerFieldDefinitions(t *testing.T) {
	schema := schemaRegistryUpdateToolSchema()
	properties := schema["properties"].(map[string]any)
	for _, required := range []string{"idempotency_key", "source_revision", "expected_active_generation", "owner_authorization"} {
		if properties[required] == nil {
			t.Fatalf("missing input %s", required)
		}
	}
	for _, forbidden := range []string{"table_id", "sheet_id", "field_id", "field_type", "records", "values"} {
		if properties[forbidden] != nil {
			t.Fatalf("caller-controlled schema input exposed: %s", forbidden)
		}
	}
	if properties["owner_authorization"].(map[string]any)["const"] != schemaRegistryAuthorization {
		t.Fatal("owner authorization constant drifted")
	}
}
