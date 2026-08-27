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
	documentAuth       map[string]any
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
		if f.documentAuth != nil {
			return map[string]any{"result": f.documentAuth}, nil
		}
		return map[string]any{"result": map[string]any{
			"errcode":         float64(0),
			"access_rule":     map[string]any{"enable_corp_internal": true},
			"secure_setting":  map[string]any{"enable_readonly_copy": false},
			"doc_member_list": []any{map[string]any{"type": float64(1), "userid": "symbolic-admin", "auth": float64(7)}},
			"co_auth_list":    []any{},
		}}, nil
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
		Version: 1, InstanceName: "zoop_wecom_zhycit", TenantRoute: "tenant-route", SchemaAdminUser: "symbolic-admin", RegistryDocumentID: fake.registryDocumentID,
		RegistryKey: "registry-key", SchemaMirrorPath: schemaPath, StatePath: filepath.Join(directory, "state.json"),
		InitializationGeneration: "generation-symbolic", SchemaVersion: catalog.Version, RegistrySheetID: fake.registrySheetID, InitializedState: "config_committed",
		APIWhitelist: map[string][]string{instanceInitializeGroup: {"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records"}},
	}
	local, err := config.LoadSchema(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime.SchemaDigest = local.Digest
	return runtime, fake
}

func TestInstanceInitializeStatusReadyIsReadOnlyAndDisclosesNoRecords(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{}
	result, err := server.instanceInitializeFacade(context.Background(), runtime, fake, nil, json.RawMessage(`{"action":"status"}`))
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

func TestInstanceInitializeStaleLocalSchemaMustNotReturnReady(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	data, err := os.ReadFile(runtime.SchemaMirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	var mirror map[string]any
	if err := json.Unmarshal(data, &mirror); err != nil {
		t.Fatal(err)
	}
	roles := mirror["roles"].(map[string]any)
	fields := roles["Z-S01"].(map[string]any)["fields"].([]any)
	fields[0].(map[string]any)["field_id"] = "SYMBOLIC_STALE_FIELD_ID"
	changed, _ := json.MarshalIndent(mirror, "", "  ")
	if err := os.WriteFile(runtime.SchemaMirrorPath, append(changed, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] == "ready" || output["preview_id"] != "" {
		t.Fatalf("stale local schema was accepted: %#v", output)
	}
	plans := fmt.Sprint(output["planned_operations"])
	if !strings.Contains(plans, "generate_schema") || !strings.Contains(plans, "local_schema_field_stale") {
		t.Fatalf("stale schema did not produce an explicit repair plan: %#v", output)
	}
}

func TestCompareInitializeSchemaChecksEveryFieldContractProperty(t *testing.T) {
	multiple := true
	base := config.Field{
		Title: "符号字段", ID: "SYMBOLIC_FIELD", Type: "FIELD_TYPE_REFERENCE",
		Options: map[string]string{"符号选项": "SYMBOLIC_OPTION"}, ReferenceTargetSheetID: "SYMBOLIC_SHEET",
		ReferenceTargetFieldID: "SYMBOLIC_TARGET_FIELD", ReferenceIsMultiple: &multiple,
	}
	observed := map[string]map[string]config.Field{"Z-S01": {base.Title: base}}
	for _, test := range []struct {
		name   string
		mutate func(*config.Field)
	}{
		{"field id", func(field *config.Field) { field.ID = "SYMBOLIC_OTHER_FIELD" }},
		{"field type", func(field *config.Field) { field.Type = "FIELD_TYPE_TEXT" }},
		{"option mapping", func(field *config.Field) { field.Options = map[string]string{"符号选项": "SYMBOLIC_OTHER_OPTION"} }},
		{"reference sheet", func(field *config.Field) { field.ReferenceTargetSheetID = "SYMBOLIC_OTHER_SHEET" }},
		{"reference field", func(field *config.Field) { field.ReferenceTargetFieldID = "SYMBOLIC_OTHER_TARGET" }},
		{"reference multiplicity", func(field *config.Field) { value := false; field.ReferenceIsMultiple = &value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			local := config.Schema{Roles: map[string]map[string]config.Field{"Z-S01": {changed.Title: changed}}}
			if differences := compareInitializeSchema(local, observed); len(differences) == 0 {
				t.Fatalf("%s difference was not detected", test.name)
			}
		})
	}
}

func TestInstanceInitializeMissingGenerationMetadataMustNotReturnReady(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	runtime.InitializationGeneration = ""
	runtime.SchemaVersion = ""
	runtime.SchemaDigest = ""
	runtime.RegistrySheetID = ""
	runtime.InitializedState = ""
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] == "ready" || !strings.Contains(fmt.Sprint(output["planned_operations"]), "commit_local_config") {
		t.Fatalf("legacy metadata omission was accepted: %#v", output)
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

func TestInstanceInitializeBusinessRecoverySentinelBindsAndVerifiesSameDocument(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.activeRows = nil
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "business", OperationID: "SYMBOLIC_OPERATION",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"recovery_business_document_id":"business-doc"}`)
	result, err := (&Server{initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}).instanceInitializeStatus(context.Background(), runtime, fake, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	observed := output["observed"].(map[string]any)
	if output["state"] != "recovery_required" || output["snapshot_complete"] != true || output["next_required_input"] != "" || observed["business_document_resolved"] != true || observed["role_sheet_count"] != 9 {
		t.Fatalf("business recovery was not bound to and verified against the sentinel asset: %#v", output)
	}
	if output["preview_id"] == "" || !strings.Contains(fmt.Sprint(output["planned_operations"]), "register_unique_active_row") {
		t.Fatalf("verified executable recovery must sign a preview: %#v", output)
	}
}

func TestInstanceInitializeBusinessRecoveryUsesJournalDocumentID(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.activeRows = nil
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "business", OperationID: "SYMBOLIC_OPERATION",
		BusinessDocumentID: fake.businessDocumentID, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	observed := output["observed"].(map[string]any)
	if output["state"] != "recovery_required" || output["next_required_input"] != "" || observed["business_document_resolved"] != true || observed["role_sheet_count"] != 9 {
		t.Fatalf("journal business document was not verified as the recovery asset: %#v", output)
	}
}

func TestInstanceInitializeBusinessResolvingRequiresCandidateAndNeverRecreates(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.activeRows = nil
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "business_resolving", AssetKind: "business", OperationID: "sent-request-no-response",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	server := &Server{initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
	withoutCandidate, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if output := withoutCandidate.(map[string]any); output["state"] != "recovery_required" || output["preview_id"] != "" || output["next_required_input"] != "recovery_business_document_id" {
		t.Fatalf("business resolving sentinel was not fail-closed: %#v", output)
	}
	before := len(fake.operations)
	withCandidate, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{"recovery_business_document_id":"business-doc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if output := withCandidate.(map[string]any); output["state"] != "recovery_required" || output["preview_id"] == "" {
		t.Fatalf("verified resolving candidate did not produce executable recovery: %#v", output)
	}
	for _, operation := range fake.operations[before:] {
		if operation == "create_smartsheet" {
			t.Fatal("resolving sentinel retried document creation")
		}
	}
}

func TestInstanceInitializeRemotePlanRequiresProtectedLocalIdentity(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.activeRows = nil
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "business", OperationID: "identity-gate",
		BusinessDocumentID: fake.businessDocumentID, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	server := &Server{initializeLocalUser: func() (string, error) { return "different-admin", nil }}
	result, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "capability_gap" || output["preview_id"] != "" || !strings.Contains(fmt.Sprint(output["conflicts"]), "initializer_local_identity_unverified") {
		t.Fatalf("unprotected local identity received a write preview: %#v", output)
	}
}

func TestInstanceInitializeReportsActualRegistryFieldCount(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.registryFields = append(fake.registryFields, map[string]any{"field_title": "extra", "field_id": "extra-id", "field_type": "FIELD_TYPE_TEXT"})
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	observed := result.(map[string]any)["observed"].(map[string]any)
	if observed["registry_field_count"] != len(registryBootstrapFields)+1 {
		t.Fatalf("registry field count is not the observed count: %#v", observed)
	}
}

func TestInstanceInitializeBusinessRecoveryRejectsUnknownDocumentBeforeReads(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.activeRows = nil
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "business", OperationID: "SYMBOLIC_OPERATION",
		BusinessDocumentID: fake.businessDocumentID, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{"recovery_business_document_id":"unknown-doc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["state"] != "conflict" || len(fake.operations) != 0 {
		t.Fatalf("unknown recovery document was read or accepted: result=%#v calls=%#v", result, fake.operations)
	}
}

func TestInstanceInitializeManagementPermissionFailsClosedOnGuessedAuthField(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	fake.documentAuth = map[string]any{"errcode": float64(0), "auth_type": "admin"}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "conflict" || output["preview_id"] != "" || !strings.Contains(fmt.Sprint(output["conflicts"]), "identity_or_auth_unverified") {
		t.Fatalf("unproven management permission was accepted: %#v", output)
	}
}

func TestInstanceInitializeApplyReadyIsIdempotentAndNeverWritesRemote(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	configPath := filepath.Join(t.TempDir(), "instance.json")
	runtimeJSON, _ := json.MarshalIndent(runtime, "", "  ")
	if err := os.WriteFile(configPath, append(runtimeJSON, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "ready", UpdatedAt: "2026-08-27T00:00:00Z"}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	paths := []string{configPath, runtime.SchemaMirrorPath, instanceInitializeJournalPath(runtime)}
	beforeFiles := map[string]struct {
		data  string
		mtime time.Time
	}{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeFiles[path] = struct {
			data  string
			mtime time.Time
		}{string(data), info.ModTime()}
	}
	server := &Server{store: config.NewStore(configPath), initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
	status, err := server.instanceInitializeFacade(context.Background(), runtime, fake, nil, json.RawMessage(`{"action":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	previewID := status.(map[string]any)["preview_id"].(string)
	expiresAt := status.(map[string]any)["expires_at"].(string)
	before := len(fake.operations)
	input, _ := json.Marshal(map[string]string{"action": "apply", "preview_id": previewID, "preview_expires_at": expiresAt, "owner_authorization": instanceInitializeAuthorization})
	result, err := server.instanceInitializeFacade(context.Background(), runtime, fake, nil, input)
	if err != nil || result.(map[string]any)["state"] != "ready" || result.(map[string]any)["enterprise_wecom_updated"] != false {
		t.Fatalf("ready apply must be idempotent: result=%#v err=%v", result, err)
	}
	for _, operation := range fake.operations[before:] {
		if operation != "get_doc_base_info" && operation != "get_doc_auth" && operation != "get_sheet" && operation != "get_fields" && operation != "get_records" {
			t.Fatalf("apply performed write operation %s", operation)
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		previous := beforeFiles[path]
		if string(data) != previous.data || !info.ModTime().Equal(previous.mtime) {
			t.Fatalf("ready no-op changed %s", path)
		}
	}
}

func TestInstanceInitializeReadyNoopPersistsPendingActiveRowConvergence(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	configPath := filepath.Join(t.TempDir(), "instance.json")
	runtimeJSON, _ := json.MarshalIndent(runtime, "", "  ")
	if err := os.WriteFile(configPath, append(runtimeJSON, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "ready", AssetKind: "business", BusinessDocumentID: fake.businessDocumentID,
		PendingRegistryRow: true, PendingRegistryOp: "pending-active-row", PendingRegistryID: "registry-row", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: config.NewStore(configPath), initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
	_, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{})
	if err != nil || result.(map[string]any)["state"] != "ready" {
		t.Fatalf("ready no-op convergence failed: result=%#v err=%v", result, err)
	}
	persisted, _, _, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if err != nil || persisted.PendingRegistryRow || persisted.Phase != "registry_row_verified" {
		t.Fatalf("ready no-op returned before pending journal was durably cleared: %#v err=%v", persisted, err)
	}
}

func TestInstanceInitializePendingActiveRowIDMismatchFailsClosed(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "ready", AssetKind: "business", BusinessDocumentID: fake.businessDocumentID,
		PendingRegistryRow: true, PendingRegistryOp: "pending-active-row", PendingRegistryID: "different-record", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	result, err := (&Server{}).instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "recovery_required" || output["preview_id"] != "" || !strings.Contains(fmt.Sprint(output["conflicts"]), "registry_row_pending_record_id_mismatch") {
		t.Fatalf("mismatched pending record id was accepted: %#v", output)
	}
	persisted, _, _, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if err != nil || !persisted.PendingRegistryRow || persisted.PendingRegistryID != "different-record" {
		t.Fatalf("mismatched pending journal was modified: %#v err=%v", persisted, err)
	}
	for _, operation := range fake.operations {
		if operation == "add_records" {
			t.Fatal("mismatched pending record id triggered a write")
		}
	}
}

func TestInstanceInitializeApplyAlwaysRequiresProtectedLocalIdentity(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	configPath := filepath.Join(t.TempDir(), "instance.json")
	runtimeJSON, _ := json.MarshalIndent(runtime, "", "  ")
	if err := os.WriteFile(configPath, append(runtimeJSON, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: config.NewStore(configPath), initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
	statusResult, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	status := statusResult.(map[string]any)
	server.initializeLocalUser = func() (string, error) { return "different-admin", nil }
	before := len(fake.operations)
	input, _ := json.Marshal(map[string]string{"preview_id": status["preview_id"].(string), "preview_expires_at": status["expires_at"].(string), "owner_authorization": instanceInitializeAuthorization})
	if _, err := server.instanceInitializeApply(context.Background(), runtime, fake, nil, input); err == nil || !strings.Contains(err.Error(), "本机受保护身份") {
		t.Fatalf("apply accepted mismatched protected local identity: %v", err)
	}
	if len(fake.operations) != before {
		t.Fatalf("identity rejection performed online calls: before=%d after=%d", before, len(fake.operations))
	}
}

func TestInstanceInitializePreviewInvalidatesWhenJournalChanges(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	server := &Server{initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
	status, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	previewID := status.(map[string]any)["preview_id"].(string)
	expiresAt := status.(map[string]any)["expires_at"].(string)
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "registry_resolving", AssetKind: "registry", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
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
	server := &Server{initializeLocalUser: func() (string, error) { return "symbolic-admin", nil }}
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
	for _, schema := range []map[string]any{instanceInitializeFacadeToolSchema(), instanceInitializeStatusToolSchema(), instanceInitializeApplyToolSchema()} {
		data, _ := json.Marshal(schema)
		if strings.Contains(string(data), "existing_business_document_id") {
			t.Fatalf("undecided business import is public: %s", data)
		}
	}
}

func TestInstanceInitializeFacadeDispatch(t *testing.T) {
	action, delegated, err := parseInstanceInitializeFacade(json.RawMessage(`{"action":"status","registry_document_id":"registry_candidate"}`))
	if err != nil || action != "status" || strings.Contains(string(delegated), "action") {
		t.Fatalf("status facade mismatch: action=%q payload=%s err=%v", action, delegated, err)
	}
	if _, _, err := parseInstanceInitializeFacade(json.RawMessage(`{"action":"status","preview_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); err == nil {
		t.Fatal("status facade accepted apply arguments")
	}
	action, delegated, err = parseInstanceInitializeFacade(json.RawMessage(`{"action":"apply","preview_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","preview_expires_at":"2026-08-27T00:00:00Z","owner_authorization":"apply_approved_instance_initialization"}`))
	if err != nil || action != "apply" || strings.Contains(string(delegated), "action") {
		t.Fatalf("apply facade mismatch: action=%q payload=%s err=%v", action, delegated, err)
	}
	if _, _, err := parseInstanceInitializeFacade(json.RawMessage(`{"action":"run"}`)); err == nil {
		t.Fatal("facade accepted unknown action")
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
	for _, required := range []string{"wecom_instance_initialize", "wecom_instance_initialize_status", "wecom_instance_initialize_apply", "\"action\"", instanceInitializeAuthorization, "preview_expires_at", "registry_document_id"} {
		if !strings.Contains(text, required) {
			t.Fatalf("tools/list missing %s: %s", required, text)
		}
	}
	if strings.Contains(text, "existing_business_document_id") {
		t.Fatalf("undecided business import leaked: %s", text)
	}
}

type initializeTemplateFake struct {
	fields     []any
	records    []any
	operations []string
}

func (f *initializeTemplateFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	f.operations = append(f.operations, operation)
	switch operation {
	case "get_fields":
		return okInitializeResponse("fields", f.fields), nil
	case "get_records":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": false, "records": f.records}}, nil
	case "delete_records":
		f.records = nil
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	case "update_fields":
		body := payload.(map[string]any)
		items := body["fields"].([]any)
		updated := items[0].(map[string]any)
		f.fields = []any{map[string]any{"field_id": updated["field_id"], "field_title": updated["field_title"], "field_type": updated["field_type"]}}
		return okInitializeResponse("fields", f.fields), nil
	default:
		return nil, fmt.Errorf("unexpected operation %s", operation)
	}
}

func TestNormalizeOwnedCreateDocSheetReusesPrimaryAndDeletesOnlyEmptyRows(t *testing.T) {
	fake := &initializeTemplateFake{
		fields:  []any{map[string]any{"field_id": "default-primary", "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"}},
		records: []any{map[string]any{"record_id": "empty-row", "values": map[string]any{"default-primary": []any{}}}},
	}
	if err := normalizeOwnedInitializeSheet(context.Background(), fake, "created-doc", "created-sheet", "需求编号"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.operations, ","); got != "get_fields,get_records,delete_records,get_records,update_fields,get_fields" {
		t.Fatalf("unexpected operation order: %s", got)
	}
	field := fake.fields[0].(map[string]any)
	if field["field_id"] != "default-primary" || field["field_title"] != "需求编号" || len(fake.records) != 0 {
		t.Fatalf("default template was not normalized safely: fields=%#v records=%#v", fake.fields, fake.records)
	}
}

func TestNormalizeOwnedCreateDocSheetRejectsUnexpectedExtraFieldWithoutCleanup(t *testing.T) {
	fake := &initializeTemplateFake{
		fields: []any{
			map[string]any{"field_id": "default-primary", "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"},
			map[string]any{"field_id": "unexpected", "field_title": "来源不明字段", "field_type": "FIELD_TYPE_TEXT"},
		},
		records: []any{map[string]any{"record_id": "empty-row", "values": map[string]any{"default-primary": []any{}}}},
	}
	if err := normalizeOwnedInitializeSheet(context.Background(), fake, "created-doc", "created-sheet", "需求编号"); err == nil || !strings.Contains(err.Error(), "来源不明字段") {
		t.Fatalf("unexpected template must fail closed: %v", err)
	}
	if got := strings.Join(fake.operations, ","); got != "get_fields" {
		t.Fatalf("field template must be validated before record cleanup: %s", got)
	}
}

func TestNormalizeOwnedAddSheetReusesOfficialDefaultPrimaryField(t *testing.T) {
	fake := &initializeTemplateFake{fields: []any{map[string]any{"field_id": "add-sheet-primary", "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"}}}
	if err := normalizeOwnedInitializeSheet(context.Background(), fake, "created-doc", "added-sheet", "任务编号"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.operations, ","); got != "get_fields,get_records,update_fields,get_fields" {
		t.Fatalf("add_sheet default template was not normalized: %s", got)
	}
	field := fake.fields[0].(map[string]any)
	if field["field_id"] != "add-sheet-primary" || field["field_title"] != "任务编号" {
		t.Fatalf("add_sheet primary field was not reused: %#v", field)
	}
}

func TestNormalizeOwnedAddSheetRejectsMissingDefaultPrimaryField(t *testing.T) {
	fake := &initializeTemplateFake{}
	if err := normalizeOwnedInitializeSheet(context.Background(), fake, "created-doc", "added-sheet", "任务编号"); err == nil || !strings.Contains(err.Error(), "数量不是 1") {
		t.Fatalf("missing add_sheet default primary must fail closed: %v", err)
	}
}

func TestNormalizeOwnedSheetNeverDeletesNonEmptyRecord(t *testing.T) {
	fake := &initializeTemplateFake{
		fields:  []any{map[string]any{"field_id": "default-primary", "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"}},
		records: []any{map[string]any{"record_id": "user-row", "values": map[string]any{"default-primary": []any{map[string]any{"text": "not empty"}}}}},
	}
	if err := normalizeOwnedInitializeSheet(context.Background(), fake, "created-doc", "created-sheet", "需求编号"); err == nil || !strings.Contains(err.Error(), "非空记录") {
		t.Fatalf("non-empty record must block cleanup: %v", err)
	}
	if strings.Contains(strings.Join(fake.operations, ","), "delete_records") {
		t.Fatalf("non-empty record was deleted: %#v", fake.operations)
	}
}

func TestInitializeFormulaPayloadFailsClosedWhenDependencyIDMissing(t *testing.T) {
	field := zoopschema.Field{
		Title: "进度条", Type: "FIELD_TYPE_FORMULA",
		Formula: &zoopschema.Formula{
			Model: []zoopschema.FormulaToken{
				{Type: "FORMULA_TYPE_FIELD", FieldTitle: "已完成任务数"},
				{Type: "FORMULA_TYPE_TEXT", Text: "/"},
				{Type: "FORMULA_TYPE_FIELD", FieldTitle: "当前任务总数"},
			},
			Formatter: zoopschema.FormulaFormatter{Type: "FIELD_TYPE_PROGRESS", DecimalPlaces: -1},
		},
	}
	fake := &initializeTemplateFake{fields: []any{map[string]any{"field_id": "completed-id", "field_title": "已完成任务数", "field_type": "FIELD_TYPE_NUMBER"}}}
	if _, err := initializeFieldWire(context.Background(), fake, "business-doc", "z-s01-sheet", field, map[string]string{}); err == nil || !strings.Contains(err.Error(), "当前任务总数") {
		t.Fatalf("formula dependency without a current field id was accepted: %v", err)
	}
	if got := strings.Join(fake.operations, ","); got != "get_fields" {
		t.Fatalf("formula dependency failure performed unexpected operations: %s", got)
	}
}

func TestRemoteApplyCannotSelfElevateInitializerCapability(t *testing.T) {
	runtime, fake := readyInitializeFixture(t)
	catalog, _ := zoopschema.Current()
	catalog.CompleteForCreation = true
	observation := initializeObservation{Snapshot: initializeSnapshot{InstanceName: runtime.InstanceName}, PlannedOperations: []string{"create_registry"}, SnapshotComplete: true}
	_, err := (&Server{}).applyRemoteInstanceInitialization(context.Background(), runtime, fake, observation, catalog, instanceInitializeJournal{Version: instanceInitializeJournalV1})
	if err == nil || !strings.Contains(err.Error(), "不会自行提升白名单") {
		t.Fatalf("write capability was self-elevated: %v", err)
	}
}

type initializeLifecycleSheet struct {
	id      string
	name    string
	fields  []any
	records []any
}

type initializeLifecycleDocument struct {
	id     string
	name   string
	sheets []*initializeLifecycleSheet
}

type initializeLifecycleFake struct {
	documents         map[string]*initializeLifecycleDocument
	operations        []string
	failOnceOperation string
	failed            bool
	sequence          int
	createCount       int
	deleteCount       int
	updateFieldCount  int
	smokeCount        int
	failFinalResolver bool
	addRecordCalls    int
	hideRegistryReads int
	hideReadsAfterAdd int
	formulaPayload    map[string]any
}

func (f *initializeLifecycleFake) nextID(prefix string) string {
	f.sequence++
	return fmt.Sprintf("%s-%03d", prefix, f.sequence)
}

func (f *initializeLifecycleFake) shouldFail(operation string) bool {
	if f.failOnceOperation == operation && !f.failed {
		f.failed = true
		return true
	}
	return false
}

func (f *initializeLifecycleFake) sheet(documentID, sheetID string) (*initializeLifecycleSheet, error) {
	document := f.documents[documentID]
	if document == nil {
		return nil, fmt.Errorf("unknown document %s", documentID)
	}
	for _, sheet := range document.sheets {
		if sheet.id == sheetID {
			return sheet, nil
		}
	}
	return nil, fmt.Errorf("unknown sheet %s", sheetID)
}

func (f *initializeLifecycleFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	f.operations = append(f.operations, operation)
	body, _ := payload.(map[string]any)
	documentID, _ := body["docid"].(string)
	sheetID, _ := body["sheet_id"].(string)
	switch operation {
	case "create_smartsheet":
		name, _ := body["doc_name"].(string)
		id := f.nextID("document")
		defaultFieldID := f.nextID("default-field")
		defaultSheet := &initializeLifecycleSheet{
			id: f.nextID("sheet"), name: "默认子表",
			fields:  []any{map[string]any{"field_id": defaultFieldID, "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"}},
			records: []any{map[string]any{"record_id": f.nextID("empty-record"), "values": map[string]any{defaultFieldID: []any{}}}},
		}
		f.documents[id] = &initializeLifecycleDocument{id: id, name: name, sheets: []*initializeLifecycleSheet{defaultSheet}}
		f.createCount++
		return map[string]any{"result": map[string]any{"errcode": float64(0), "docid": id}}, nil
	case "get_doc_base_info":
		document := f.documents[documentID]
		if document == nil {
			return nil, fmt.Errorf("unknown document %s", documentID)
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "doc_type": float64(10), "doc_name": document.name}}, nil
	case "get_doc_auth":
		return map[string]any{"result": map[string]any{
			"errcode": float64(0), "access_rule": map[string]any{"enable_corp_internal": true},
			"secure_setting":  map[string]any{"enable_readonly_copy": false},
			"doc_member_list": []any{map[string]any{"type": float64(1), "userid": "test-admin", "auth": float64(7)}}, "co_auth_list": []any{},
		}}, nil
	case "get_sheet":
		document := f.documents[documentID]
		if document == nil {
			return nil, fmt.Errorf("unknown document %s", documentID)
		}
		if f.failFinalResolver && f.smokeCount >= 4 && document.name != "SMART_SHEETS_IDS" {
			return okInitializeResponse("sheet_list", []any{}), nil
		}
		items := []any{}
		for _, sheet := range document.sheets {
			items = append(items, map[string]any{"type": "smartsheet", "sheet_id": sheet.id, "name": sheet.name})
		}
		return okInitializeResponse("sheet_list", items), nil
	case "update_sheet":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		properties, _ := body["properties"].(map[string]any)
		sheet.name, _ = properties["title"].(string)
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	case "add_sheet":
		document := f.documents[documentID]
		properties, _ := body["properties"].(map[string]any)
		name, _ := properties["title"].(string)
		defaultFieldID := f.nextID("default-field")
		createdSheetID := f.nextID("sheet")
		document.sheets = append(document.sheets, &initializeLifecycleSheet{
			id: createdSheetID, name: name,
			fields:  []any{map[string]any{"field_id": defaultFieldID, "field_title": "默认主字段", "field_type": "FIELD_TYPE_TEXT"}},
			records: []any{map[string]any{"record_id": f.nextID("empty-record"), "values": map[string]any{defaultFieldID: []any{}}}},
		})
		if f.shouldFail(operation) {
			return nil, fmt.Errorf("injected uncertain add_sheet")
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "sheet_id": createdSheetID}}, nil
	case "get_fields":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		return okInitializeResponse("fields", sheet.fields), nil
	case "update_fields":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		updates, _ := body["fields"].([]any)
		for _, updateRaw := range updates {
			update, _ := updateRaw.(map[string]any)
			for _, fieldRaw := range sheet.fields {
				field, _ := fieldRaw.(map[string]any)
				if field["field_id"] == update["field_id"] {
					field["field_title"] = update["field_title"]
					field["field_type"] = update["field_type"]
				}
			}
		}
		f.updateFieldCount++
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	case "add_fields":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		items, _ := body["fields"].([]any)
		for _, itemRaw := range items {
			item, _ := itemRaw.(map[string]any)
			if item["field_title"] == "进度条" {
				f.formulaPayload, _ = item["property_formula"].(map[string]any)
			}
			field := map[string]any{"field_id": f.nextID("field"), "field_title": item["field_title"], "field_type": item["field_type"]}
			for key, value := range item {
				if key == "property_single_select" {
					property, _ := value.(map[string]any)
					options, _ := property["options"].([]any)
					readbackOptions := make([]any, 0, len(options))
					for _, rawOption := range options {
						option, _ := rawOption.(map[string]any)
						readbackOptions = append(readbackOptions, map[string]any{"id": f.nextID("option"), "name": option["text"]})
					}
					field[key] = map[string]any{"options": readbackOptions}
					continue
				}
				if strings.HasPrefix(key, "property_") {
					field[key] = value
				}
			}
			sheet.fields = append(sheet.fields, field)
		}
		if f.shouldFail(operation) {
			return nil, fmt.Errorf("injected uncertain add_fields")
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	case "get_records":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(sheet.name, "Z-S01｜") {
			f.smokeCount++
		}
		if document := f.documents[documentID]; document != nil && document.name == "SMART_SHEETS_IDS" && f.hideRegistryReads > 0 {
			f.hideRegistryReads--
			return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": false, "records": []any{}}}, nil
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "has_more": false, "records": sheet.records}}, nil
	case "delete_records":
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		sheet.records = nil
		f.deleteCount++
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	case "add_records":
		f.addRecordCalls++
		if f.hideReadsAfterAdd > 0 {
			f.hideRegistryReads = f.hideReadsAfterAdd
			f.hideReadsAfterAdd = 0
		}
		sheet, err := f.sheet(documentID, sheetID)
		if err != nil {
			return nil, err
		}
		items, _ := body["records"].([]any)
		createdRecords := []any{}
		for _, itemRaw := range items {
			item, _ := itemRaw.(map[string]any)
			record := map[string]any{"record_id": f.nextID("record"), "values": item["values"]}
			sheet.records = append(sheet.records, record)
			createdRecords = append(createdRecords, record)
		}
		if f.shouldFail(operation) {
			return nil, fmt.Errorf("injected uncertain add_records")
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "records": createdRecords}}, nil
	}
	return nil, fmt.Errorf("unexpected lifecycle request %s %#v", operation, payload)
}

func syntheticInitializeCatalog() zoopschema.Catalog {
	catalog := zoopschema.Catalog{Version: "zoop-v1", SourceContract: "synthetic-test", CompleteForCreation: true}
	for index := 1; index <= 9; index++ {
		roleName := fmt.Sprintf("Z-S%02d", index)
		primary := fmt.Sprintf("主字段%02d", index)
		catalog.Roles = append(catalog.Roles, zoopschema.Role{
			Role: roleName, SheetTitle: roleName + "｜测试表", PrimaryFieldTitle: primary,
			Fields: []zoopschema.Field{{Title: primary, Type: "FIELD_TYPE_TEXT"}, {Title: fmt.Sprintf("说明%02d", index), Type: "FIELD_TYPE_TEXT"}},
		})
	}
	return catalog
}

func initializeLifecycleFixture(t *testing.T) (config.Config, zoopschema.Catalog, *initializeLifecycleFake, *Server, initializeObservation, instanceInitializeJournal) {
	t.Helper()
	directory := t.TempDir()
	operations := []string{"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records", "create_smartsheet", "add_sheet", "update_sheet", "add_fields", "update_fields", "add_records", "delete_records"}
	runtime := config.Config{
		Version: 1, InstanceName: "initialize-test", TenantRoute: "test-route", SchemaAdminUser: "test-admin", RegistryKey: "test-registry",
		SchemaMirrorPath: filepath.Join(directory, "not-yet-created.json"), StatePath: filepath.Join(directory, "state.json"),
		APIWhitelist: map[string][]string{instanceInitializeGroup: operations},
	}
	configPath := filepath.Join(directory, "instance.json")
	data, err := json.MarshalIndent(runtime, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := syntheticInitializeCatalog()
	fake := &initializeLifecycleFake{documents: map[string]*initializeLifecycleDocument{}}
	observation := initializeObservation{
		State: "changes_planned", SnapshotComplete: true, PlannedOperations: []string{"create_registry"},
		Snapshot: initializeSnapshot{InstanceName: runtime.InstanceName, ConfigDigest: runtime.Digest(), CatalogDigest: digestValue(catalog), CatalogVersion: catalog.Version, CatalogCreationComplete: true, RoleSheetIDs: map[string]string{}, RoleFieldsDigests: map[string]string{}},
	}
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "schema_staged", CatalogDigest: digestValue(catalog), ConfigDigest: runtime.Digest(), UpdatedAt: "2026-08-27T00:00:00Z"}
	return runtime, catalog, fake, &Server{
		store: config.NewStore(configPath), initializeCatalog: func() (zoopschema.Catalog, error) { return catalog, nil },
		initializeLocalUser: func() (string, error) { return "test-admin", nil },
	}, observation, journal
}

func publicInitializeStatusAndApply(t *testing.T, server *Server, runtime config.Config, fake wecomRequester, input map[string]string) (map[string]any, any, error) {
	t.Helper()
	statusRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	statusResult, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, statusRaw)
	if err != nil {
		t.Fatal(err)
	}
	status := statusResult.(map[string]any)
	previewID, _ := status["preview_id"].(string)
	expiresAt, _ := status["expires_at"].(string)
	if previewID == "" || expiresAt == "" {
		t.Fatalf("public status did not sign an executable preview: %#v", status)
	}
	applyInput := map[string]string{"preview_id": previewID, "preview_expires_at": expiresAt, "owner_authorization": instanceInitializeAuthorization}
	for key, value := range input {
		applyInput[key] = value
	}
	applyRaw, err := json.Marshal(applyInput)
	if err != nil {
		t.Fatal(err)
	}
	result, applyErr := server.instanceInitializeApply(context.Background(), runtime, fake, nil, applyRaw)
	return status, result, applyErr
}

func TestRemoteInitializerControlledEndToEnd(t *testing.T) {
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	status, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, _ := json.Marshal(status)
	for _, internalID := range []string{"document-001", "sheet-003"} {
		if strings.Contains(string(statusJSON), internalID) {
			t.Fatalf("public status disclosed internal ID %s: %s", internalID, statusJSON)
		}
	}
	output := result.(map[string]any)
	if output["state"] != "ready" || output["enterprise_wecom_updated"] != true || output["local_updated"] != true || output["config_backup_created"] != true {
		t.Fatalf("full initializer did not reach ready: %#v", output)
	}
	if fake.createCount != 2 || fake.deleteCount != 10 || fake.updateFieldCount != 10 || fake.smokeCount < 3 {
		t.Fatalf("unexpected lifecycle evidence: create=%d delete=%d update=%d smoke=%d", fake.createCount, fake.deleteCount, fake.updateFieldCount, fake.smokeCount)
	}
	loadedJournal, _, exists, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if err != nil || !exists || loadedJournal.Phase != "ready" {
		t.Fatalf("ready journal missing: journal=%#v exists=%v err=%v", loadedJournal, exists, err)
	}
	persisted, err := server.store.Current()
	if err != nil || persisted.RegistryDocumentID == "" || persisted.RegistrySheetID == "" || persisted.InitializedState != "config_committed" {
		t.Fatalf("initialized config was not committed: %#v err=%v", persisted, err)
	}
}

func TestRemoteInitializerRecoversPartialAssetsWithoutDuplicateCreate(t *testing.T) {
	for _, failure := range []string{"add_fields", "add_sheet"} {
		t.Run(failure, func(t *testing.T) {
			runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
			fake.failOnceOperation = failure
			if _, _, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{}); err == nil {
				t.Fatal("injected partial failure unexpectedly succeeded")
			}
			createdBeforeRecovery := fake.createCount
			stored, _, exists, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
			if err != nil || !exists || stored.Phase != "recovery_required" || stored.RegistryDocumentID == "" {
				t.Fatalf("partial failure did not persist recovery identity: %#v exists=%v err=%v", stored, exists, err)
			}
			if failure == "add_sheet" && stored.BusinessDocumentID == "" {
				t.Fatalf("business recovery lost docid: %#v", stored)
			}
			recoveryInput := map[string]string{}
			if failure == "add_fields" {
				recoveryInput["recovery_registry_document_id"] = stored.RegistryDocumentID
			} else {
				recoveryInput["recovery_business_document_id"] = stored.BusinessDocumentID
			}
			status, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, recoveryInput)
			if err != nil {
				t.Fatal(err)
			}
			if status["state"] != "recovery_required" {
				t.Fatalf("public recovery status was not explicit: %#v", status)
			}
			statusJSON, _ := json.Marshal(status)
			for _, internalID := range []string{stored.RegistryDocumentID, stored.BusinessDocumentID} {
				if internalID != "" && strings.Contains(string(statusJSON), internalID) {
					t.Fatalf("public recovery status disclosed internal ID %s: %s", internalID, statusJSON)
				}
			}
			if result.(map[string]any)["state"] != "ready" {
				t.Fatalf("recovery did not reach ready: %#v", result)
			}
			expectedCreates := 2
			if failure == "add_fields" {
				expectedCreates = createdBeforeRecovery + 1
			} else if fake.createCount != createdBeforeRecovery {
				t.Fatalf("business recovery repeated create: before=%d after=%d", createdBeforeRecovery, fake.createCount)
			}
			if fake.createCount != expectedCreates {
				t.Fatalf("recovery created wrong number of documents: failure=%s before=%d after=%d", failure, createdBeforeRecovery, fake.createCount)
			}
			if failure == "add_sheet" {
				preservedUnownedTemplate := false
				for _, document := range fake.documents {
					for _, sheet := range document.sheets {
						if strings.HasPrefix(sheet.name, "Z-S02｜") && len(sheet.records) == 1 && len(sheet.fields) > 0 && sheet.fields[0].(map[string]any)["field_title"] == "默认主字段" {
							preservedUnownedTemplate = true
						}
					}
				}
				if !preservedUnownedTemplate {
					t.Fatal("uncertain add_sheet response was incorrectly treated as owned and cleaned")
				}
			}
		})
	}
}

func TestRemoteInitializerTreatsRegisterRowAsRemoteAndConvergesUncertainWrite(t *testing.T) {
	if !hasRemoteInitializePlan([]string{"verify_recovered_business_document", "register_unique_active_row"}) {
		t.Fatal("register_unique_active_row was misclassified as a local-only operation")
	}
	if requiresCompleteInitializeCatalog([]string{"verify_recovered_business_document", "register_unique_active_row"}) {
		t.Fatal("active-row-only recovery was incorrectly blocked by an unrelated formula creation gap")
	}
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	fake.failOnceOperation = "add_records"
	_, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["state"] != "ready" || !fake.failed {
		t.Fatalf("uncertain active row write did not converge by readback: %#v", result)
	}
}

func TestRemoteInitializerNeverRepeatsTemporarilyInvisibleActiveRowWrite(t *testing.T) {
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	fake.failOnceOperation = "add_records"
	fake.hideReadsAfterAdd = 2
	if _, _, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{}); err == nil {
		t.Fatal("temporarily invisible active row write unexpectedly converged")
	}
	journal, _, exists, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if err != nil || !exists || !journal.PendingRegistryRow || journal.BusinessDocumentID == "" || fake.addRecordCalls != 1 {
		t.Fatalf("active row pending sentinel missing: journal=%#v calls=%d err=%v", journal, fake.addRecordCalls, err)
	}
	statusRaw, _ := json.Marshal(map[string]string{"recovery_business_document_id": journal.BusinessDocumentID})
	statusResult, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, statusRaw)
	if err != nil {
		t.Fatal(err)
	}
	status := statusResult.(map[string]any)
	if status["state"] != "recovery_required" || status["preview_id"] != "" || !strings.Contains(fmt.Sprint(status["conflicts"]), "registry_row_write_uncertain_unresolved") || fake.addRecordCalls != 1 {
		t.Fatalf("invisible active row did not remain read-only recovery: status=%#v add_calls=%d", status, fake.addRecordCalls)
	}
	_, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{"recovery_business_document_id": journal.BusinessDocumentID})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["state"] != "ready" || fake.addRecordCalls != 1 {
		t.Fatalf("visible pending active row did not converge without duplicate add: result=%#v add_calls=%d", result, fake.addRecordCalls)
	}
	convergedJournal, _, _, err := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if err != nil || convergedJournal.PendingRegistryRow || convergedJournal.Phase != "ready" {
		t.Fatalf("active row pending journal did not converge: %#v err=%v", convergedJournal, err)
	}
	registry := fake.documents[journal.RegistryDocumentID]
	if registry == nil || len(registry.sheets[0].records) != 1 {
		t.Fatalf("active row recovery formed duplicate rows: %#v", registry)
	}
}

func TestInstanceInitializeFreshPublicMainlineKeepsFormulaWriteFailClosed(t *testing.T) {
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	server.initializeCatalog = nil
	result, err := server.instanceInitializeFacade(context.Background(), runtime, fake, nil, json.RawMessage(`{"action":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	status := result.(map[string]any)
	if status["state"] != "capability_gap" || status["preview_id"] != "" || status["catalog_creation_complete"] != false {
		t.Fatalf("fresh public mainline did not keep formula write fail-closed: %#v", status)
	}
}

func TestInstanceInitializeRegistryRecoveryWithMissingFieldsBlocksUnprovenFormulaWrite(t *testing.T) {
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	server.initializeCatalog = nil
	registryResponse, err := fake.Request(context.Background(), "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "SMART_SHEETS_IDS"})
	if err != nil {
		t.Fatal(err)
	}
	registryID := initializeCreatedDocumentID(registryResponse)
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "registry", OperationID: "registry-missing-fields",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	operationStart := len(fake.operations)
	statusRaw, _ := json.Marshal(map[string]string{"recovery_registry_document_id": registryID})
	result, err := server.instanceInitializeStatus(context.Background(), runtime, fake, nil, statusRaw)
	if err != nil {
		t.Fatal(err)
	}
	status := result.(map[string]any)
	if status["state"] != "capability_gap" || status["capability_gap"] != true || status["preview_id"] != "" || !strings.Contains(fmt.Sprint(status["conflicts"]), "downstream_business_state_unproven") {
		t.Fatalf("unproven formula write did not block Registry recovery: %#v", status)
	}
	applyRaw, _ := json.Marshal(map[string]string{
		"preview_id": strings.Repeat("0", 64), "preview_expires_at": time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		"owner_authorization": instanceInitializeAuthorization, "recovery_registry_document_id": registryID,
	})
	if _, err := server.instanceInitializeApply(context.Background(), runtime, fake, nil, applyRaw); err == nil {
		t.Fatal("apply accepted an unknown preview")
	}
	if fake.createCount != 1 {
		t.Fatalf("read-only status created a downstream business document: create_count=%d", fake.createCount)
	}
	for _, operation := range fake.operations[operationStart:] {
		switch operation {
		case "create_smartsheet", "add_sheet", "update_sheet", "add_fields", "update_fields", "add_records", "delete_records":
			t.Fatalf("read-only status performed remote write %s", operation)
		}
	}
}

func TestInstanceInitializeRecoveryCandidateWithoutJournalDocIDRemainsUnowned(t *testing.T) {
	runtime, catalog, fake, server, _, _ := initializeLifecycleFixture(t)
	registryResponse, err := fake.Request(context.Background(), "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "SMART_SHEETS_IDS"})
	if err != nil {
		t.Fatal(err)
	}
	registryID := initializeCreatedDocumentID(registryResponse)
	bootstrapJournal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "registry_identity_known", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := reconcileInitializeRegistry(context.Background(), fake, registryID, true, &bootstrapJournal, instanceInitializeJournalPath(runtime)); err != nil {
		t.Fatal(err)
	}
	businessResponse, err := fake.Request(context.Background(), "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "Zoop｜test-registry"})
	if err != nil {
		t.Fatal(err)
	}
	businessID := initializeCreatedDocumentID(businessResponse)
	original := fake.documents[businessID].sheets[0]
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "business", OperationID: "unowned-candidate",
		RegistryDocumentID: registryID, CatalogDigest: digestValue(catalog), ConfigDigest: runtime.Digest(), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	fake.deleteCount, fake.updateFieldCount = 0, 0
	status, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{"recovery_business_document_id": businessID})
	if err != nil {
		t.Fatal(err)
	}
	if status["state"] != "recovery_required" || result.(map[string]any)["state"] != "ready" {
		t.Fatalf("unowned candidate recovery did not complete: status=%#v result=%#v", status, result)
	}
	if original.name != "默认子表" || len(original.fields) != 1 || original.fields[0].(map[string]any)["field_title"] != "默认主字段" || len(original.records) != 1 {
		t.Fatalf("unowned recovery candidate was cleaned or renamed: %#v", original)
	}
	if fake.deleteCount != 9 || fake.updateFieldCount != 9 {
		t.Fatalf("cleanup escaped current-operation-created sheets: delete=%d update=%d", fake.deleteCount, fake.updateFieldCount)
	}
}

func TestInstanceInitializeRegistryRecoveryCandidateWithoutJournalDocIDRemainsUnowned(t *testing.T) {
	runtime, catalog, fake, server, _, _ := initializeLifecycleFixture(t)
	registryResponse, err := fake.Request(context.Background(), "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "SMART_SHEETS_IDS"})
	if err != nil {
		t.Fatal(err)
	}
	registryID := initializeCreatedDocumentID(registryResponse)
	original := fake.documents[registryID].sheets[0]
	journal := instanceInitializeJournal{
		Version: instanceInitializeJournalV1, Phase: "recovery_required", AssetKind: "registry", OperationID: "unowned-registry-candidate",
		CatalogDigest: digestValue(catalog), ConfigDigest: runtime.Digest(), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		t.Fatal(err)
	}
	fake.deleteCount, fake.updateFieldCount = 0, 0
	status, result, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{"recovery_registry_document_id": registryID})
	if err != nil {
		t.Fatal(err)
	}
	if status["state"] != "recovery_required" || result.(map[string]any)["state"] != "ready" {
		t.Fatalf("unowned Registry candidate recovery did not complete: status=%#v result=%#v", status, result)
	}
	defaultRecordPreserved := false
	for _, raw := range original.records {
		record, _ := raw.(map[string]any)
		if strings.HasPrefix(fmt.Sprint(record["record_id"]), "empty-record-") {
			defaultRecordPreserved = true
		}
	}
	if original.name != "默认子表" || !defaultRecordPreserved || original.fields[0].(map[string]any)["field_title"] != "默认主字段" {
		t.Fatalf("unowned Registry recovery candidate was cleaned or renamed: %#v", original)
	}
	if fake.deleteCount != 9 || fake.updateFieldCount != 9 {
		t.Fatalf("Registry candidate cleanup escaped current-operation-created business sheets: delete=%d update=%d", fake.deleteCount, fake.updateFieldCount)
	}
}

func TestInstanceInitializeFinalSmokeMustUseReloadedConfigResolver(t *testing.T) {
	runtime, _, fake, server, _, _ := initializeLifecycleFixture(t)
	fake.failFinalResolver = true
	_, _, err := publicInitializeStatusAndApply(t, server, runtime, fake, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "正常 Resolver") {
		t.Fatalf("final smoke trusted observation IDs instead of the reloaded Resolver: %v", err)
	}
	persisted, loadErr := server.store.Current()
	if loadErr != nil || persisted.InitializedState != "config_committed" {
		t.Fatalf("negative Resolver test did not reach committed config: %#v err=%v", persisted, loadErr)
	}
	journal, _, exists, journalErr := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if journalErr != nil || !exists || journal.Phase != "config_committed" {
		t.Fatalf("Resolver failure did not retain forward-recovery journal: %#v exists=%v err=%v", journal, exists, journalErr)
	}
}
