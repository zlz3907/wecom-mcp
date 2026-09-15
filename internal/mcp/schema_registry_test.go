package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

type schemaRegistryReadFailClient struct{}

func (schemaRegistryReadFailClient) Request(context.Context, string, any) (map[string]any, error) {
	return nil, errors.New("lookup attempted")
}

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

func TestSchemaRegistryReadSchemaIsConvenientAndReadOnly(t *testing.T) {
	schema := schemaRegistryReadToolSchema()
	if required, ok := schema["required"]; ok && len(required.([]string)) != 0 {
		t.Fatalf("read tool unexpectedly requires arguments: %#v", required)
	}
	properties := schema["properties"].(map[string]any)
	if properties["generation"].(map[string]any)["default"] != "active" {
		t.Fatal("read tool must default to active generation")
	}
	if properties["entry_type"].(map[string]any)["default"] != "field" {
		t.Fatal("read tool must default to field entries")
	}
	if properties["limit"].(map[string]any)["default"] != 200 || properties["compact"].(map[string]any)["default"] != true {
		t.Fatal("read tool must request a convenient compact active-generation page by default")
	}
	if properties["max_bytes"].(map[string]any)["default"] != defaultQueryBytes {
		t.Fatal("read tool must protect WorkBuddy from oversized responses")
	}
	if access := teamToolAccess["wecom_schema_registry_read"]; access != ToolAccessReader {
		t.Fatalf("registry read access=%s, want reader", access)
	}
}

func TestSchemaRegistryReadAcceptsOmittedArguments(t *testing.T) {
	server := New(filepath.Join(t.TempDir(), "unused-config.json"))
	runtime := config.Config{
		RegistryDocumentID: "registry", RegistryKey: "registry-key",
		APIWhitelist: map[string][]string{"schema_registry": {"get_sheet", "get_fields", "get_records"}},
	}
	_, err := server.readSchemaRegistry(context.Background(), runtime, schemaRegistryReadFailClient{}, json.RawMessage(nil))
	if err == nil || err.Error() != "lookup attempted" {
		t.Fatalf("omitted arguments did not reach the read path: %v", err)
	}
}

func TestSchemaRegistryStatusExplainsActiveGenerationAndRecordCounts(t *testing.T) {
	table, runtime, generation := schemaRegistryTestTable(t)
	result := schemaRegistryStatusResult(runtime, table)
	if result["state"] != "active" || result["registry_sheet_id"] != "registry-sheet" {
		t.Fatalf("unexpected status identity: %#v", result)
	}
	if result["active_generation"] != generation || result["schema_digest"] != generation {
		t.Fatalf("active generation metadata missing: %#v", result)
	}
	if result["source_revision"] != "probe-revision" || result["captured_at"] != "2026-09-14T08:01:11Z" {
		t.Fatalf("source metadata missing: %#v", result)
	}
	if result["active_table_count"] != 2 || result["active_field_count"] != 2 {
		t.Fatalf("active counts are incomplete: %#v", result)
	}
	breakdown := result["record_count_breakdown"].(map[string]int)
	if breakdown["total"] != 4 || breakdown["active_pointer"] != 1 || breakdown["manifest_records"] != 1 || breakdown["field_records"] != 2 {
		t.Fatalf("unexpected record breakdown: %#v", breakdown)
	}
	if len(result["registry_fields"].([]map[string]any)) != 28 {
		t.Fatalf("registry schema was not returned: %#v", result["registry_fields"])
	}
	tables := result["tables"].([]map[string]any)
	if len(tables) != 2 || tables[0]["target_role"] != "Z-S01" || tables[1]["target_role"] != "Z-S02" {
		t.Fatalf("unexpected table summaries: %#v", tables)
	}
}

func TestSchemaRegistryReadFiltersAndSortsFriendlyEntries(t *testing.T) {
	table, _, generation := schemaRegistryTestTable(t)
	entries := schemaRegistryFilteredEntries(table, generation, schemaRegistryReadInput{EntryType: "field"})
	if len(entries) != 2 || entries[0].TargetRole != "Z-S01" || entries[1].TargetRole != "Z-S02" {
		t.Fatalf("unexpected complete read order: %#v", entries)
	}
	entries = schemaRegistryFilteredEntries(table, generation, schemaRegistryReadInput{EntryType: "field", TargetRole: "Z-S02"})
	if len(entries) != 1 || entries[0].TableID != "sheet-2" || entries[0].FieldID != "field-2" {
		t.Fatalf("role filter did not return friendly identifiers: %#v", entries)
	}
	entries = schemaRegistryFilteredEntries(table, generation, schemaRegistryReadInput{EntryType: "field", Query: "NUMBER"})
	if len(entries) != 1 || entries[0].FieldName != "数量" || entries[0].WriteCodec != "number" {
		t.Fatalf("query filter did not match type/codec: %#v", entries)
	}
	entries = schemaRegistryFilteredEntries(table, generation, schemaRegistryReadInput{EntryType: "manifest"})
	if len(entries) != 1 || entries[0].SourceRevision != "probe-revision" || entries[0].FieldCount != "2" {
		t.Fatalf("manifest read is incomplete: %#v", entries)
	}
	entries = schemaRegistryFilteredEntries(table, generation, schemaRegistryReadInput{EntryType: "all"})
	if len(entries) != 3 {
		t.Fatalf("all must include manifest + fields but exclude mutable active pointer: %#v", entries)
	}
}

func TestSchemaRegistryGenerationReadStateRejectsMissingAndIncompleteSnapshots(t *testing.T) {
	table, _, generation := schemaRegistryTestTable(t)
	state, metadata := schemaRegistryGenerationReadState(table, strings.Repeat("f", 64))
	if state != "generation_not_found" || len(metadata) != 0 {
		t.Fatalf("missing generation was treated as readable: state=%s metadata=%#v", state, metadata)
	}
	manifestKey := "manifest:" + generation
	manifest := table.ByKey[manifestKey]
	delete(table.ByKey, manifestKey)
	state, _ = schemaRegistryGenerationReadState(table, generation)
	if state != "generation_not_found" {
		t.Fatalf("generation without manifest state=%s", state)
	}
	table.ByKey[manifestKey] = manifest
	for key, record := range table.ByKey {
		if schemaRegistryEntry(table, record).EntryType == "field" {
			delete(table.ByKey, key)
			break
		}
	}
	state, metadata = schemaRegistryGenerationReadState(table, generation)
	if state != "generation_incomplete" || len(metadata) != 0 {
		t.Fatalf("incomplete generation was treated as readable: state=%s metadata=%#v", state, metadata)
	}
}

func TestCompactSchemaRegistryEntryIsUsefulAndBounded(t *testing.T) {
	entry := schemaRegistryEntryView{
		EntryKey: strings.Repeat("k", 64), EntryType: "field", Generation: strings.Repeat("a", 64),
		TargetRole: "Z-S01", TableName: "Z-S01｜需求", TableID: "sheet-1", FieldName: "标题", FieldID: "field-1",
		FieldType: "FIELD_TYPE_TEXT", RawFieldProperties: strings.Repeat("x", 5000), WriteCodec: "text_cell_array",
	}
	compact := compactSchemaRegistryEntry(entry)
	if compact["raw_field_properties"] != nil || compact["field_id"] != "field-1" {
		t.Fatalf("compact registry entry is not useful and bounded: %#v", compact)
	}
}

func TestCompactSchemaRegistryEntryPublishesSelectOptionsAndMetadataAvailability(t *testing.T) {
	entry := schemaRegistryEntryView{
		EntryType: "field", TargetRole: "Z-S09", FieldName: "AI 工具平台", FieldID: "platform",
		FieldType: "FIELD_TYPE_SINGLE_SELECT", IsPrimary: "未知", Options: `{"OpenAI Codex":"option-codex"}`,
	}
	compact := compactSchemaRegistryEntry(entry)
	options := compact["options"].(map[string]string)
	if options["OpenAI Codex"] != "option-codex" || compact["options_available"] != true {
		t.Fatalf("compact select options missing: %#v", compact)
	}
	if compact["primary_metadata_available"] != false {
		t.Fatalf("unknown primary metadata must be explicit: %#v", compact)
	}

	entry.Options = ""
	compact = compactSchemaRegistryEntry(entry)
	if compact["options_available"] != false || compact["options"] != nil {
		t.Fatalf("missing select options must not be guessed: %#v", compact)
	}

	entry.FieldType = "FIELD_TYPE_TEXT"
	entry.IsPrimary = "否"
	compact = compactSchemaRegistryEntry(entry)
	if compact["options_available"] != nil || compact["primary_metadata_available"] != true {
		t.Fatalf("non-select metadata flags are wrong: %#v", compact)
	}
}

func TestSchemaRegistryPageStopsBeforeMaxBytesAndCanResume(t *testing.T) {
	compact := true
	pageInput := schemaRegistryReadInput{EntryType: "field", Limit: 20, Compact: &compact, MaxBytes: 1024}
	generation := strings.Repeat("a", 64)
	result := map[string]any{
		"entries": []any{}, "returned_count": 0, "has_more": true, "response_truncated": false,
		"next_page": schemaRegistryNextPage(pageInput, generation, 7),
	}
	page := []any{}
	accepted := true
	for index := 0; index < 20 && accepted; index++ {
		page, accepted = appendSchemaRegistryPage(result, page, map[string]any{
			"field_id": strconv.Itoa(index), "field_name": strings.Repeat("字段", 30),
		}, 7, 27, 1024)
	}
	if accepted || len(page) == 0 || len(mustMarshal(result)) > 1024 {
		t.Fatalf("page did not stop safely: accepted=%t bytes=%d result=%#v", accepted, len(mustMarshal(result)), result)
	}
	if result["response_truncated"] != true || result["next_offset"] != 7+len(page) {
		t.Fatalf("page cannot be resumed: %#v", result)
	}
	firstCount := len(page)
	next := result["next_offset"].(int)
	nextPage := result["next_page"].(map[string]any)
	if nextPage["generation"] != generation || nextPage["offset"] != next {
		t.Fatalf("next page is not bound to the concrete generation: %#v", nextPage)
	}
	second := map[string]any{
		"entries": []any{}, "returned_count": 0, "has_more": true, "response_truncated": false,
		"next_page": schemaRegistryNextPage(pageInput, generation, next),
	}
	secondPage := []any{}
	secondAccepted := true
	for index := firstCount; index < 20 && secondAccepted; index++ {
		secondPage, secondAccepted = appendSchemaRegistryPage(second, secondPage, map[string]any{
			"field_id": strconv.Itoa(index), "field_name": strings.Repeat("字段", 30),
		}, next, 27, 1024)
	}
	if len(secondPage) == 0 || secondPage[0].(map[string]any)["field_id"] != strconv.Itoa(firstCount) {
		t.Fatalf("next_offset repeated or skipped an entry: first=%d next=%d second=%#v", firstCount, next, secondPage)
	}
}

func TestSchemaRegistryBaseMetadataHonorsMaxBytes(t *testing.T) {
	result := map[string]any{"source_revision": strings.Repeat("x", 1100), "entries": []any{}}
	if err := validateSchemaRegistryResultSize(result, 1024); err == nil || !strings.Contains(err.Error(), "基础元数据") {
		t.Fatalf("oversized empty response was not rejected accurately: %v", err)
	}
}

func schemaRegistryTestTable(t *testing.T) (schemaRegistryTable, config.Config, string) {
	t.Helper()
	runtime := config.Config{InstanceName: "instance", RegistryKey: "registry"}
	fieldIDs := map[string]string{}
	registryFields := make([]map[string]any, 0, len(schemaRegistryMigrationFields()))
	for index, field := range schemaRegistryMigrationFields() {
		id := "registry-field-" + strconv.Itoa(index)
		fieldIDs[field.Title] = id
		registryFields = append(registryFields, map[string]any{
			"field_title": field.Title, "field_id": id, "field_type": "FIELD_TYPE_TEXT", "is_primary": index == 0,
		})
	}
	snapshot := schemaRegistrySnapshot{
		CapturedAt: "2026-09-14T08:01:11Z",
		FieldCount: 2,
		Entries: []map[string]string{
			{
				"条目类型": "field", "实例名称": "instance", "Registry Key": "registry", "表角色": "Z-S01",
				"表名": "Z-S01｜需求", "表 ID": "sheet-1", "字段名": "标题", "字段 ID": "field-1",
				"字段类型": "FIELD_TYPE_TEXT", "是否允许新增": "是", "是否允许更新": "是", "写入编码器": "text_cell_array", "Codec 验证状态": "已验证",
			},
			{
				"条目类型": "field", "实例名称": "instance", "Registry Key": "registry", "表角色": "Z-S02",
				"表名": "Z-S02｜项目", "表 ID": "sheet-2", "字段名": "数量", "字段 ID": "field-2",
				"字段类型": "FIELD_TYPE_NUMBER", "是否允许新增": "是", "是否允许更新": "是", "写入编码器": "number", "Codec 验证状态": "已验证",
			},
		},
	}
	var err error
	snapshot.Generation, err = schemaRegistryEntriesDigest(snapshot.Entries)
	if err != nil {
		t.Fatal(err)
	}
	expected := schemaRegistryGenerationRecords(snapshot, runtime, "probe-revision", fieldIDs)
	byKey := map[string]map[string]any{}
	records := []any{}
	for key, values := range expected {
		record := map[string]any{"record_id": "record-" + key, "values": values}
		byKey[key] = record
		records = append(records, record)
	}
	pointerValues := schemaRegistryPointerValues(snapshot, runtime, "probe-revision", fieldIDs)
	pointer := map[string]any{"record_id": "record-active", "values": pointerValues}
	byKey[schemaRegistryActiveKey] = pointer
	records = append(records, pointer)
	return schemaRegistryTable{
		Target: wecom.Target{SheetID: "registry-sheet", Role: schemaRegistryRole},
		Fields: registryFields, FieldIDs: fieldIDs, Records: records, ByKey: byKey,
		ActiveGeneration: snapshot.Generation, ActiveRecordID: "record-active", ActiveComplete: true, ActiveFieldCount: 2,
	}, runtime, snapshot.Generation
}

func TestRuntimeSchemaUsesCompleteActiveGeneration(t *testing.T) {
	table, _, generation := schemaRegistryTestTable(t)
	for index := 3; index <= 9; index++ {
		role := fmt.Sprintf("Z-S0%d", index)
		key := "field:" + generation + ":" + role + ":field-" + strconv.Itoa(index)
		values := map[string]any{}
		for title, value := range map[string]string{
			"Schema 条目键": key, "条目类型": "field", "Schema 版本": generation,
			"生效状态": "ready", "实例名称": "instance", "Registry Key": "registry", "Schema Digest": generation,
			"表角色": role, "表名": role + "｜fixture", "表 ID": "sheet-" + strconv.Itoa(index),
			"字段名": "字段" + strconv.Itoa(index), "字段 ID": "field-" + strconv.Itoa(index),
			"字段类型": "FIELD_TYPE_TEXT", "是否允许新增": "是", "是否允许更新": "是",
			"写入编码器": "text_cell_array", "Codec 验证状态": "已验证",
		} {
			values[table.FieldIDs[title]] = []any{map[string]any{"text": value}}
		}
		table.ByKey[key] = map[string]any{"record_id": "record-" + key, "values": values}
	}
	table.ActiveFieldCount = 9
	runtime := config.Config{InstanceName: "instance", RegistryKey: "registry"}
	snapshot, err := runtimeSchemaFromRegistryTable(table, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != generation || snapshot.Schema.Digest != generation || len(snapshot.Schema.Roles) != 9 {
		t.Fatalf("runtime snapshot did not bind the active generation: %#v", snapshot)
	}
	if snapshot.Schema.Roles["Z-S02"]["数量"].ID != "field-2" {
		t.Fatalf("field identity was not loaded from Z-S00: %#v", snapshot.Schema.Roles["Z-S02"])
	}
	table.ActiveComplete = false
	if _, err := runtimeSchemaFromRegistryTable(table, runtime); err == nil {
		t.Fatal("incomplete active generation was accepted")
	}
}

func TestRuntimeSchemaDoesNotSilentlyFallBackToLocalMirror(t *testing.T) {
	runtime := config.Config{
		SchemaMirrorPath: filepath.Join(t.TempDir(), "schema.json"), RegistryDocumentID: "registry", RegistryKey: "key",
		APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}},
	}
	_, err := loadRuntimeSchema(context.Background(), runtime, schemaRegistryReadFailClient{})
	if err == nil || !strings.Contains(err.Error(), "lookup attempted") {
		t.Fatalf("online runtime silently fell back to local Schema: %v", err)
	}
}
