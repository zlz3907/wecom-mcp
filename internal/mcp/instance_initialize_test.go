package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/zoopschema"
)

type initializeFakeClient struct {
	registryDocumentID string
	businessDocumentID string
	registrySheetID    string
	roleSheetIDs       map[string]string
	registryFields     []any
	roleFields         map[string][]any
	activeRows         []any
	registryIncomplete bool
	operations         []string
}

func (f *initializeFakeClient) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	f.operations = append(f.operations, operation)
	body, _ := payload.(map[string]any)
	documentID, _ := body["docid"].(string)
	sheetID, _ := body["sheet_id"].(string)
	switch operation {
	case "get_doc_base_info":
		name := "Zoop｜测试实例"
		if documentID == f.registryDocumentID {
			name = "SMART_SHEETS_IDS"
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "doc_type": float64(10), "doc_name": name}}, nil
	case "get_doc_auth":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "auth_type": "admin"}}, nil
	case "get_sheet":
		if documentID == f.registryDocumentID {
			return okInitializeResponse("sheet_list", []any{map[string]any{"type": "smartsheet", "sheet_id": f.registrySheetID, "name": "SMART_SHEETS_IDS"}}), nil
		}
		if documentID == f.businessDocumentID {
			items := []any{}
			catalog, _ := zoopschema.Current()
			for _, role := range catalog.Roles {
				if id := f.roleSheetIDs[role.Role]; id != "" {
					items = append(items, map[string]any{"type": "smartsheet", "sheet_id": id, "name": role.SheetTitle})
				}
			}
			return okInitializeResponse("sheet_list", items), nil
		}
	case "get_fields":
		if documentID == f.registryDocumentID && sheetID == f.registrySheetID {
			return okInitializeResponse("fields", f.registryFields), nil
		}
		for role, id := range f.roleSheetIDs {
			if documentID == f.businessDocumentID && sheetID == id {
				return okInitializeResponse("fields", f.roleFields[role]), nil
			}
		}
	case "get_records":
		if documentID == f.registryDocumentID && sheetID == f.registrySheetID {
			if f.registryIncomplete {
				offset, _ := body["offset"].(int)
				if offset > 0 {
					return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": true, "records": []any{}}}, nil
				}
				return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": true, "records": f.activeRows}}, nil
			}
			return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": false, "records": f.activeRows}}, nil
		}
		if documentID == f.businessDocumentID && sheetID == f.roleSheetIDs["Z-S01"] {
			return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": false, "records": []any{}}}, nil
		}
	}
	return nil, fmt.Errorf("unexpected request %s %#v", operation, payload)
}

func okInitializeResponse(key string, value any) map[string]any {
	return map[string]any{"result": map[string]any{"errcode": float64(0), key: value}}
}

func readyInitializeFixture(t *testing.T) (config.Config, *initializeFakeClient) {
	t.Helper()
	directory := t.TempDir()
	catalog, err := zoopschema.Current()
	if err != nil {
		t.Fatal(err)
	}
	fieldsByRole := map[string][]config.Field{}
	fake := &initializeFakeClient{
		registryDocumentID: "registry-doc", businessDocumentID: "business-doc", registrySheetID: "registry-sheet",
		roleSheetIDs: map[string]string{}, roleFields: map[string][]any{},
	}
	for _, title := range registryBootstrapFields {
		fake.registryFields = append(fake.registryFields, map[string]any{"field_title": title, "field_id": "id-" + title, "field_type": "FIELD_TYPE_TEXT"})
	}
	fake.activeRows = []any{map[string]any{
		"record_id": "registry-row", "values": map[string]any{
			"id-registry_key":     []any{map[string]any{"text": "registry-key"}},
			"id-lifecycle_status": []any{map[string]any{"text": "active"}},
			"id-docid":            []any{map[string]any{"text": fake.businessDocumentID}},
		},
	}}
	fieldIDsByRole := map[string]map[string]string{}
	for _, role := range catalog.Roles {
		fake.roleSheetIDs[role.Role] = "sheet-" + role.Role
		fieldIDsByRole[role.Role] = map[string]string{}
		for index, field := range role.Fields {
			id := fmt.Sprintf("field-%s-%03d", role.Role, index)
			fieldIDsByRole[role.Role][field.Title] = id
		}
	}
	for _, role := range catalog.Roles {
		for _, field := range role.Fields {
			id := fieldIDsByRole[role.Role][field.Title]
			raw := map[string]any{"field_title": field.Title, "field_id": id, "field_type": field.Type}
			if len(field.Options) > 0 {
				options := []any{}
				for index, option := range field.Options {
					options = append(options, map[string]any{"id": fmt.Sprintf("option-%s-%d", role.Role, index), "name": option})
				}
				raw["property_single_select"] = map[string]any{"options": options}
			}
			if field.Reference != nil {
				raw["property_reference"] = map[string]any{
					"sub_id": fake.roleSheetIDs[field.Reference.Role], "field_id": fieldIDsByRole[field.Reference.Role][field.Reference.FieldTitle], "is_multiple": field.Reference.Multiple,
				}
			}
			fake.roleFields[role.Role] = append(fake.roleFields[role.Role], raw)
			mirror := config.Field{Title: field.Title, ID: id, Type: field.Type}
			if len(field.Options) > 0 {
				mirror.Options = map[string]string{}
				for index, option := range field.Options {
					mirror.Options[option] = fmt.Sprintf("option-%s-%d", role.Role, index)
				}
			}
			if field.Reference != nil {
				multiple := field.Reference.Multiple
				mirror.ReferenceTargetSheetID = fake.roleSheetIDs[field.Reference.Role]
				mirror.ReferenceTargetFieldID = fieldIDsByRole[field.Reference.Role][field.Reference.FieldTitle]
				mirror.ReferenceIsMultiple = &multiple
			}
			fieldsByRole[role.Role] = append(fieldsByRole[role.Role], mirror)
		}
	}
	schemaPath := filepath.Join(directory, "schema.json")
	if err := config.WriteOnlineMirror(schemaPath, fieldsByRole, "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	runtime := config.Config{
		Version: 1, InstanceName: "zoop_wecom_zhycit", TenantRoute: "tenant-route", RegistryDocumentID: fake.registryDocumentID,
		RegistryKey: "registry-key", SchemaMirrorPath: schemaPath, StatePath: filepath.Join(directory, "state.json"),
		APIWhitelist: map[string][]string{instanceInitializeGroup: {"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records"}},
	}
	return runtime, fake
}

func TestInstanceInitializeStatusReadyIsReadOnlyAndDisclosesNoRecords(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{}
	result, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "ready" || output["enterprise_wecom_updated"] != false || output["local_updated"] != false || output["business_records_disclosed"] != false {
		t.Fatalf("unexpected result: %#v", output)
	}
	for _, operation := range fake.operations {
		if operation != "get_doc_base_info" && operation != "get_doc_auth" && operation != "get_sheet" && operation != "get_fields" && operation != "get_records" {
			t.Fatalf("status performed write operation %s", operation)
		}
	}
	encoded, _ := json.Marshal(output)
	if strings.Contains(string(encoded), "registry-row") || strings.Contains(string(encoded), "business-doc") {
		t.Fatalf("managed record or business id leaked in public status: %s", encoded)
	}
}

func TestInstanceInitializeRequiresDedicatedCapabilityGroup(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	runtime.APIWhitelist = map[string][]string{"other": {"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records"}}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["state"] != "environment_unavailable" || len(fake.operations) != 0 {
		t.Fatalf("dedicated capability was bypassed: result=%#v calls=%#v", result, fake.operations)
	}
}

func TestInstanceInitializeIncompletePaginationHasNoUsablePreview(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.registryIncomplete = true
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "conflict" || output["snapshot_complete"] != false || output["preview_id"] != "" || output["expires_at"] != "" {
		t.Fatalf("incomplete pagination produced a usable preview: %#v", output)
	}
}

func TestInstanceInitializeRegistryImportIsSeparateFromRecovery(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	runtime.RegistryDocumentID = ""
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{"registry_document_id":"registry-doc"}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "ready" || output["snapshot_complete"] != true || output["preview_id"] == "" {
		t.Fatalf("validated registry import did not produce a complete dry-run: %#v", output)
	}
}

func TestInstanceInitializeUnresolvedRecoveryHasNoUsablePreview(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	runtime.RegistryDocumentID = ""
	legacy := registryBootstrapState{Phase: "creating", StartedAt: "2026-08-27T00:00:00Z", UpdatedAt: "2026-08-27T00:00:00Z"}
	if err := reserveRegistryBootstrapState(registryBootstrapStatePath(runtime), legacy); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "recovery_required" || output["snapshot_complete"] != false || output["preview_id"] != "" || output["next_required_input"] != "recovery_registry_document_id" {
		t.Fatalf("uncertain recovery was not fail-closed: %#v", output)
	}
	if len(fake.operations) != 0 {
		t.Fatal("unbound recovery must not guess an online document")
	}
}

func TestInstanceInitializeRecoveryIDRequiresUncertainSentinel(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{"recovery_registry_document_id":"other-registry"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["state"] != "environment_unavailable" && result.(map[string]any)["state"] != "conflict" {
		t.Fatalf("unexpected state: %#v", result)
	}
	if len(fake.operations) != 0 {
		t.Fatal("conflicting recovery id must stop before online reads")
	}
}

func TestInstanceInitializeApplyIsFailClosedAndNeverWrites(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{}
	status, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	previewID := status.(map[string]any)["preview_id"].(string)
	expiresAt := status.(map[string]any)["expires_at"].(string)
	before := len(fake.operations)
	input, _ := json.Marshal(map[string]string{"preview_id": previewID, "preview_expires_at": expiresAt, "owner_authorization": instanceInitializeAuthorization})
	if _, err := server.instanceInitializeApply(context.Background(), runtime, fake, nil, input); err == nil || !strings.Contains(err.Error(), "Gate 1") {
		t.Fatalf("apply must fail closed at architecture gate: %v", err)
	}
	for _, operation := range fake.operations[before:] {
		if operation != "get_doc_base_info" && operation != "get_doc_auth" && operation != "get_sheet" && operation != "get_fields" && operation != "get_records" {
			t.Fatalf("apply performed write operation %s", operation)
		}
	}
}

func TestInstanceInitializePreviewInvalidatesWhenJournalChanges(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{}
	status, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	previewID := status.(map[string]any)["preview_id"].(string)
	expiresAt := status.(map[string]any)["expires_at"].(string)
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "registry_resolving", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"preview_id": previewID, "preview_expires_at": expiresAt, "owner_authorization": instanceInitializeAuthorization})
	if _, err := server.instanceInitializeApply(context.Background(), runtime, fake, nil, input); err == nil || !strings.Contains(err.Error(), "预览已失效") {
		t.Fatalf("journal change did not invalidate preview: %v", err)
	}
}

func TestInstanceInitializeApplyRequiresExactPreviewExpiry(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{}
	status, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	previewID := status.(map[string]any)["preview_id"].(string)
	input, _ := json.Marshal(map[string]string{"preview_id": previewID, "preview_expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), "owner_authorization": instanceInitializeAuthorization})
	if _, err := server.instanceInitializeApply(context.Background(), runtime, fake, nil, input); err == nil || !strings.Contains(err.Error(), "preview_expires_at") {
		t.Fatalf("mismatched expiry was accepted: %v", err)
	}
}

func TestInitializeOutputJournalAndPreviewContainNoSecretCanary(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	const secret = "SECRET-CANARY-DO-NOT-LEAK"
	server := &Server{}
	result, err := server.instanceInitializeStatus(context.Background(), runtime, fake, fmt.Errorf("upstream %s", secret), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), secret) {
		t.Fatalf("secret leaked in status: %s", data)
	}
	journalPath := filepath.Join(t.TempDir(), "state", "journal.json")
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "registry", LastErrorCode: "upstream_unavailable", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := saveInstanceInitializeJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	journalData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journalData), secret) {
		t.Fatal("secret leaked into journal")
	}
	if info, _ := os.Stat(journalPath); info.Mode().Perm() != 0600 {
		t.Fatalf("journal mode=%o", info.Mode().Perm())
	}
}

func TestInitializeJournalRejectsUnknownAndInvalidRecoveryState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "journal.json")
	data := `{"version":"instance-initialize-v1","phase":"ready","updated_at":"2026-08-27T00:00:00Z","GNAS_APP_SECRET":"SECRET-CANARY"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadInstanceInitializeJournal(path); err == nil {
		t.Fatal("journal with unknown secret-like field must fail closed")
	}
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "recovery_required", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := saveInstanceInitializeJournal(path, journal); err == nil {
		t.Fatal("recovery journal without asset kind must fail closed")
	}
}

func TestInitializerToolSchemaDoesNotExposeUndecidedBusinessImport(t *testing.T) {
	for _, schema := range []map[string]any{instanceInitializeStatusToolSchema(), instanceInitializeApplyToolSchema()} {
		data, _ := json.Marshal(schema)
		if strings.Contains(string(data), "existing_business_document_id") {
			t.Fatalf("undecided business import is public: %s", data)
		}
	}
}

func TestStdioToolsListExposesInitializerContracts(t *testing.T) {
	server := &Server{}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	response := server.Handle(context.Background(), request)
	if response == nil || response.Error != nil {
		t.Fatalf("tools/list failed: %#v", response)
	}
	data, _ := json.Marshal(response.Result)
	text := string(data)
	for _, required := range []string{"wecom_instance_initialize_status", "wecom_instance_initialize_apply", instanceInitializeAuthorization, "preview_expires_at", "registry_document_id"} {
		if !strings.Contains(text, required) {
			t.Fatalf("tools/list missing %s: %s", required, text)
		}
	}
	if strings.Contains(text, "existing_business_document_id") {
		t.Fatalf("undecided business import leaked: %s", text)
	}
}
