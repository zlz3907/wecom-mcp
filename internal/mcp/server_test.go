package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestCompileRecordsOnlyUsesSupportedMirrorCodecs(t *testing.T) {
	schema := config.Schema{Roles: map[string]map[string]config.Field{
		"Z-S01": {
			"需求标题":   {Title: "需求标题", ID: "fTitle", Type: "FIELD_TYPE_TEXT"},
			"自动规划授权": {Title: "自动规划授权", ID: "fAuto", Type: "FIELD_TYPE_CHECKBOX"},
			"需求阶段":   {Title: "需求阶段", ID: "fState", Type: "FIELD_TYPE_SINGLE_SELECT"},
		},
	}}
	got, err := compileRecords(schema, applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"需求标题": "MCP 重构", "自动规划授权": true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["values"].(map[string]any)["fAuto"] != true {
		t.Fatalf("checkbox was not compiled")
	}
	_, err = compileRecords(schema, applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"需求阶段": "待确认"}}}})
	if err == nil {
		t.Fatal("select write without an owner-synced codec must fail")
	}
}

func TestCompileRecordsUsesOfficialSingleSelectTextObject(t *testing.T) {
	schema := config.Schema{Roles: map[string]map[string]config.Field{"Z-S03": {
		"任务状态": {Title: "任务状态", ID: "status", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"执行中": "option-id"}},
	}}}
	got, err := compileRecords(schema, applyInput{TargetRole: "Z-S03", Operation: "update_records", Records: []recordInput{{RecordID: "task", Values: map[string]any{"任务状态": "执行中"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]string{"text": "执行中"}}
	if canonicalValue(got[0]["values"].(map[string]any)["status"]) != canonicalValue(want) {
		t.Fatalf("single-select codec=%#v, want %#v", got, want)
	}
}

func TestReserveRejectsDuplicateAndChangedIntent(t *testing.T) {
	s := &Server{}
	path := t.TempDir() + "/state.json"
	if err := s.reserve(path, "req-123456789012", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.reserve(path, "req-123456789012", "a"); err == nil {
		t.Fatal("pending request must not be repeated")
	}
	if err := s.reserve(path, "req-123456789012", "b"); err == nil {
		t.Fatal("same key with changed intent must fail")
	}
}

func TestReservePreservesEntriesAcrossServerInstances(t *testing.T) {
	path := t.TempDir() + "/state.json"
	servers := []*Server{{}, {}}
	keys := []string{"req-aaaaaaaaaaaa", "req-bbbbbbbbbbbb"}
	start := make(chan struct{})
	errors := make(chan error, len(servers))
	var wait sync.WaitGroup
	for index, server := range servers {
		index, server := index, server
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errors <- server.reserve(path, keys[index], keys[index])
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Entries) != 2 {
		t.Fatalf("entries=%#v", state.Entries)
	}
}

func TestCompleteStateReportsCorruptPersistence(t *testing.T) {
	server := &Server{}
	path := t.TempDir() + "/state.json"
	if err := server.reserve(path, "req-cccccccccccc", "digest"); err != nil {
		t.Fatal(err)
	}
	if err := saveState(path, idempotencyState{Entries: nil}); err != nil {
		t.Fatal(err)
	}
	if err := server.completeState(path, "req-cccccccccccc", "digest"); err == nil {
		t.Fatal("invalid persisted state must not be reported as completed")
	}
}

func TestRecordApplyBindsConfiguredOperatorWithoutCallerActor(t *testing.T) {
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", IdempotencyKey: "idempotency-key-0001", SourceRevision: "rev-1"}
	prepared := []map[string]any{{"values": map[string]any{"field": "value"}}}
	if requestDigest(input, prepared, "operator-a") == requestDigest(input, prepared, "operator-b") {
		t.Fatal("request digest did not bind business operator userid")
	}
	server := &Server{}
	path := filepath.Join(t.TempDir(), "state.json")
	digest := requestDigest(input, prepared, "operator-a")
	if err := server.reserveWithOperator(path, input.IdempotencyKey, digest, "operator-a"); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(path)
	if err != nil || state.Entries[input.IdempotencyKey].BusinessOperatorUserID != "operator-a" {
		t.Fatalf("operator missing from idempotency state: %#v err=%v", state, err)
	}
	if err := server.completeStateWithOperator(path, input.IdempotencyKey, digest, "operator-b"); err == nil {
		t.Fatal("different operator completed reserved mutation")
	}
	if _, err := server.apply(context.Background(), config.Config{}, config.Schema{}, nil, json.RawMessage(`{"target_role":"Z-S01","operation":"add_records","idempotency_key":"idempotency-key-0001","source_revision":"rev-1","records":[{"values":{"x":"y"}}]}`)); err == nil || !strings.Contains(err.Error(), "wecom_operator_userid") {
		t.Fatalf("record write without configured operator was not fail-closed: %v", err)
	}
	for _, item := range tools {
		if item.Name != "wecom_record_apply" {
			continue
		}
		encoded, _ := json.Marshal(item.InputSchema)
		if strings.Contains(string(encoded), "actor") || strings.Contains(string(encoded), "operator_userid") {
			t.Fatalf("caller-controllable actor leaked into tool schema: %s", encoded)
		}
		return
	}
	t.Fatal("wecom_record_apply tool not found")
}

func TestVerifyReadbackRequiresExactRecordAndValues(t *testing.T) {
	prepared := []map[string]any{{"record_id": "record-1", "values": map[string]any{"field-1": []map[string]string{{"type": "text", "text": "ok"}}}}}
	readback := map[string]any{"result": map[string]any{"records": []any{map[string]any{"record_id": "record-1", "values": map[string]any{"field-1": []any{map[string]any{"type": "text", "text": "ok"}}}}}}}
	if !verifyReadback("update_records", prepared, nil, readback) {
		t.Fatal("expected exact readback to pass")
	}
	wrong := map[string]any{"result": map[string]any{"records": []any{map[string]any{"record_id": "record-1", "values": map[string]any{"field-1": []any{map[string]any{"type": "text", "text": "changed"}}}}}}}
	if verifyReadback("update_records", prepared, nil, wrong) {
		t.Fatal("changed value must fail readback")
	}
}

func TestCreateDocumentKeepsUpstreamDocIDAndURL(t *testing.T) {
	response := map[string]any{"result": map[string]any{
		"errcode": 0,
		"docid":   "doc-created-by-wecom",
		"url":     "https://doc.weixin.qq.com/smartsheet/s3_example",
	}}
	docID, url, err := createdDocumentIdentity(response)
	if err != nil {
		t.Fatal(err)
	}
	if docID != "doc-created-by-wecom" || url != "https://doc.weixin.qq.com/smartsheet/s3_example" {
		t.Fatalf("create_doc receipt was changed: docid=%q url=%q", docID, url)
	}
}

func TestLegacyOperationsAreExposedWithoutCompatibilityAlias(t *testing.T) {
	operations := legacyOperations()
	if len(operations) != 8 {
		t.Fatalf("got %d legacy read operations, want 8", len(operations))
	}
	for _, required := range []string{"list_employees", "get_doc_base_info", "get_views", "get_records"} {
		found := false
		for _, operation := range operations {
			if operation == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"add_records", "add_fields", "delete_records"} {
		for _, operation := range operations {
			if operation == forbidden {
				t.Fatalf("generic API must not expose write operation %s", forbidden)
			}
		}
	}
}

func TestCompileRecordsUsesVerifiedFieldCodecShapes(t *testing.T) {
	schema := config.Schema{Roles: map[string]map[string]config.Field{"Z-S01": {
		"数字": {Title: "数字", ID: "number", Type: "FIELD_TYPE_NUMBER"},
		"日期": {Title: "日期", ID: "date", Type: "FIELD_TYPE_DATE_TIME"},
		"多选": {Title: "多选", ID: "select", Type: "FIELD_TYPE_SELECT"},
		"位置": {Title: "位置", ID: "location", Type: "FIELD_TYPE_LOCATION"},
	}}}
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{
		"数字": float64(88.8), "日期": "1786204800000", "多选": []any{map[string]any{"id": "option"}}, "位置": []any{map[string]any{"id": "location"}},
	}}}}
	got, err := compileRecords(schema, input)
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["values"].(map[string]any)["number"] != float64(88.8) {
		t.Fatal("number codec was not preserved")
	}
	if _, ok := got[0]["values"].(map[string]any)["select"].([]any); !ok {
		t.Fatal("select codec was not preserved")
	}
}

func TestReferenceCellUsesVerifiedStringArrayWireContract(t *testing.T) {
	got := referenceCell("record-target")
	if len(got) != 1 || got[0] != "record-target" {
		t.Fatalf("unexpected reference value: %#v", got)
	}
}

func TestCompileRecordsAcceptsControlledReferenceObjectsAndEmitsStringArray(t *testing.T) {
	schema := config.Schema{Roles: map[string]map[string]config.Field{"Z-S03": {
		"主需求": {Title: "主需求", ID: "requirement", Type: "FIELD_TYPE_REFERENCE"},
	}}}
	input := applyInput{TargetRole: "Z-S03", Operation: "add_records", Records: []recordInput{{Values: map[string]any{
		"主需求": []any{map[string]any{"record_id": "requirement-record"}},
	}}}}
	got, err := compileRecords(schema, input)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalValue(got[0]["values"].(map[string]any)["requirement"]) != canonicalValue([]any{"requirement-record"}) {
		t.Fatalf("unexpected compiled reference: %#v", got)
	}
	_, err = compileRecords(schema, applyInput{TargetRole: "Z-S03", Operation: "add_records", Records: []recordInput{{Values: map[string]any{
		"主需求": []any{"requirement-record"},
	}}}})
	if err == nil {
		t.Fatal("string-array references must be rejected in favor of the canonical object form")
	}
}

func TestReferenceReadbackGapIsCompletedWriteNotRetryableFailure(t *testing.T) {
	prepared := []map[string]any{{"record_id": "task-1", "values": map[string]any{
		"text":      []map[string]string{{"type": "text", "text": "updated"}},
		"state":     []any{map[string]string{"text": "执行中"}},
		"reference": []any{"requirement-1"},
	}}}
	readback := map[string]any{"result": map[string]any{"records": []any{map[string]any{
		"record_id": "task-1", "values": map[string]any{
			"text":      []any{map[string]any{"type": "text", "text": "updated"}},
			"state":     []any{map[string]any{"id": "option-1", "style": float64(1), "text": "执行中"}},
			"reference": []any{},
		},
	}}}}
	if !referenceReadbackGap("update_records", prepared, nil, readback) {
		t.Fatal("expected known empty-reference readback gap to be recognized")
	}
	readback["result"].(map[string]any)["records"].([]any)[0].(map[string]any)["values"].(map[string]any)["text"] = []any{map[string]any{"type": "text", "text": "wrong"}}
	if referenceReadbackGap("update_records", prepared, nil, readback) {
		t.Fatal("ordinary-field mismatch must not be accepted as a reference gap")
	}
}

func TestMirrorFieldsPreservesReferenceTarget(t *testing.T) {
	fields, err := mirrorFields([]map[string]any{
		{
			"field_title": "所属项目",
			"field_id":    "project",
			"field_type":  "FIELD_TYPE_REFERENCE",
			"property_reference": map[string]any{
				"sub_id":      "project-sheet",
				"field_id":    "project-title",
				"is_multiple": false,
			},
		},
		{
			"field_title": "主需求",
			"field_id":    "requirement",
			"field_type":  "FIELD_TYPE_REFERENCE",
			"property_referece": map[string]any{
				"sub_id": "requirement-sheet",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].ReferenceTargetSheetID != "project-sheet" || fields[0].ReferenceTargetFieldID != "project-title" || fields[0].ReferenceIsMultiple == nil || *fields[0].ReferenceIsMultiple || fields[1].ReferenceTargetSheetID != "requirement-sheet" {
		t.Fatalf("reference target was not preserved: %#v", fields)
	}
}

func TestSameCellValueNormalizesReferenceReadback(t *testing.T) {
	expected := []any{map[string]string{"record_id": "record-b"}, map[string]string{"record_id": "record-a"}}
	actual := []any{"record-a", "record-b"}
	if !sameCellValue(actual, expected) {
		t.Fatal("object-array write and string-array readback must be equivalent")
	}
	if sameCellValue([]any{"record-c"}, expected) {
		t.Fatal("different reference IDs must not be equivalent")
	}
}

func TestSchemaDifferencesDetectsAddedRemovedAndChangedFields(t *testing.T) {
	local := config.Schema{Roles: map[string]map[string]config.Field{}}
	online := map[string][]config.Field{}
	for index := 1; index <= 8; index++ {
		role := fmt.Sprintf("Z-S0%d", index)
		local.Roles[role] = map[string]config.Field{"保持": {Title: "保持", ID: "keep", Type: "FIELD_TYPE_TEXT"}}
		online[role] = []config.Field{{Title: "保持", ID: "keep", Type: "FIELD_TYPE_TEXT"}}
	}
	local.Roles["Z-S01"]["删除"] = config.Field{Title: "删除", ID: "removed", Type: "FIELD_TYPE_TEXT"}
	local.Roles["Z-S01"]["改变"] = config.Field{Title: "改变", ID: "changed", Type: "FIELD_TYPE_TEXT"}
	online["Z-S01"] = append(online["Z-S01"],
		config.Field{Title: "新增", ID: "added", Type: "FIELD_TYPE_NUMBER"},
		config.Field{Title: "改变", ID: "changed", Type: "FIELD_TYPE_NUMBER"},
	)
	differences, onlineCounts, localCounts := schemaDifferences(local, online)
	if len(differences) != 3 {
		t.Fatalf("differences=%#v", differences)
	}
	if onlineCounts["Z-S01"] != 3 || localCounts["Z-S01"] != 3 {
		t.Fatalf("counts online=%v local=%v", onlineCounts, localCounts)
	}
}

func TestContractDigestIsStableAcrossFieldOrder(t *testing.T) {
	first := map[string][]config.Field{"Z-S01": {
		{Title: "B", ID: "b", Type: "FIELD_TYPE_TEXT"},
		{Title: "A", ID: "a", Type: "FIELD_TYPE_TEXT"},
	}}
	second := map[string][]config.Field{"Z-S01": {
		{Title: "A", ID: "a", Type: "FIELD_TYPE_TEXT"},
		{Title: "B", ID: "b", Type: "FIELD_TYPE_TEXT"},
	}}
	if contractDigest(first) != contractDigest(second) {
		t.Fatal("contract digest must not depend on field order")
	}
}

func TestSchemaProbeStateFailsClosedWhenCountsOrDigestDiffer(t *testing.T) {
	counts := map[string]int{"Z-S01": 1}
	if state := schemaProbeState(nil, counts, counts, "same", "same"); state != "no_drift" {
		t.Fatalf("state=%s, want no_drift", state)
	}
	if state := schemaProbeState(nil, map[string]int{"Z-S01": 2}, counts, "same", "same"); state != "drift_detected" {
		t.Fatalf("count mismatch state=%s", state)
	}
	if state := schemaProbeState(nil, counts, counts, "online", "local"); state != "drift_detected" {
		t.Fatalf("digest mismatch state=%s", state)
	}
}

func TestValidateUniqueFieldsRejectsDuplicateTitleAndID(t *testing.T) {
	if err := validateUniqueFields([]config.Field{
		{Title: "重复", ID: "a", Type: "FIELD_TYPE_TEXT"},
		{Title: "重复", ID: "b", Type: "FIELD_TYPE_TEXT"},
	}); err == nil {
		t.Fatal("duplicate title must fail closed")
	}
	if err := validateUniqueFields([]config.Field{
		{Title: "一", ID: "same", Type: "FIELD_TYPE_TEXT"},
		{Title: "二", ID: "same", Type: "FIELD_TYPE_TEXT"},
	}); err == nil {
		t.Fatal("duplicate field ID must fail closed")
	}
}

func TestSchemaDifferencesDetectsOptionAndReferenceChanges(t *testing.T) {
	multiple := true
	local := config.Schema{Roles: map[string]map[string]config.Field{}}
	online := map[string][]config.Field{}
	for index := 1; index <= 8; index++ {
		role := fmt.Sprintf("Z-S0%d", index)
		local.Roles[role] = map[string]config.Field{}
		online[role] = []config.Field{}
	}
	local.Roles["Z-S01"]["状态"] = config.Field{Title: "状态", ID: "state", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"旧": "old"}}
	local.Roles["Z-S01"]["项目"] = config.Field{Title: "项目", ID: "project", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "old-sheet", ReferenceIsMultiple: &multiple}
	online["Z-S01"] = []config.Field{
		{Title: "状态", ID: "state", Type: "FIELD_TYPE_SINGLE_SELECT", Options: map[string]string{"新": "new"}},
		{Title: "项目", ID: "project", Type: "FIELD_TYPE_REFERENCE", ReferenceTargetSheetID: "new-sheet", ReferenceIsMultiple: &multiple},
	}
	differences, _, _ := schemaDifferences(local, online)
	if len(differences) != 2 || differences[0]["change"] != "changed_online" || differences[1]["change"] != "changed_online" {
		t.Fatalf("attribute changes=%#v", differences)
	}
}

func TestSchemaProbeRejectsNonEmptyArguments(t *testing.T) {
	if err := empty(json.RawMessage(`{"target_role":"Z-S01"}`)); err == nil {
		t.Fatal("probe must reject routing arguments")
	}
}
