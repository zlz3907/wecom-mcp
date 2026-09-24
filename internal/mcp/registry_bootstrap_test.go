package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

type bootstrapFakeClient struct {
	fields             map[string]map[string]any
	createCalls        int
	createBody         map[string]any
	employee           map[string]any
	employeeErr        error
	rows               []any
	addRecordCalls     int
	lostRecordResponse bool
	hideRecordWrite    bool
	incomplete         bool
	deletedFields      []string
}

func (f *bootstrapFakeClient) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	switch operation {
	case "get_employee":
		if body, ok := payload.(map[string]any); !ok || len(body) != 1 || body["userid"] != "operator-user" {
			return nil, fmt.Errorf("invalid employee payload")
		}
		if f.employeeErr != nil {
			return nil, f.employeeErr
		}
		if f.employee != nil {
			return map[string]any{"result": f.employee}, nil
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "userid": "operator-user", "status": float64(1)}}, nil
	case "list_employees":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "userlist": []any{map[string]any{"userid": "operator-user", "status": float64(1)}}}}, nil
	case "create_smartsheet":
		f.createCalls++
		f.createBody, _ = payload.(map[string]any)
		return map[string]any{"result": map[string]any{"docid": "registry-created", "url": "https://example.invalid/registry"}}, nil
	case "get_doc_auth":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "doc_member_list": []any{map[string]any{"type": float64(1), "userid": "operator-user", "auth": float64(7)}}}}, nil
	case "get_sheet":
		return map[string]any{"result": map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "sheet-registry"}}}}, nil
	case "get_fields":
		if len(f.fields) == 0 {
			f.fields = map[string]map[string]any{"文本": {"field_id": "default-text", "field_title": "文本", "field_type": "FIELD_TYPE_TEXT"}}
		}
		items := make([]any, 0, len(f.fields))
		for _, field := range f.fields {
			items = append(items, field)
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i].(map[string]any)["field_title"].(string), items[j].(map[string]any)["field_title"].(string)
			if a == "文本" {
				return true
			}
			if b == "文本" {
				return false
			}
			return a < b
		})
		return map[string]any{"result": map[string]any{"fields": items}}, nil
	case "get_records":
		body, _ := payload.(map[string]any)
		rows := f.rows
		if offset, _ := body["offset"].(int); offset > 0 {
			rows = []any{}
		}
		return map[string]any{"result": map[string]any{"errcode": 0, "records": rows, "has_more": f.incomplete}}, nil
	case "delete_records":
		body := payload.(map[string]any)
		ids := body["record_ids"].([]string)
		remaining := []any{}
		for _, raw := range f.rows {
			drop := false
			for _, id := range ids {
				if raw.(map[string]any)["record_id"] == id {
					drop = true
				}
			}
			if !drop {
				remaining = append(remaining, raw)
			}
		}
		f.rows = remaining
		return map[string]any{"result": map[string]any{"errcode": 0}}, nil
	case "delete_fields":
		body := payload.(map[string]any)
		for _, id := range body["field_ids"].([]string) {
			if id == "default-text" {
				return nil, fmt.Errorf("primary cannot be deleted")
			}
			f.deletedFields = append(f.deletedFields, id)
			for title, field := range f.fields {
				if field["field_id"] == id {
					delete(f.fields, title)
				}
			}
		}
		return map[string]any{"result": map[string]any{"errcode": 0}}, nil
	case "update_fields":
		body := payload.(map[string]any)
		for _, raw := range body["fields"].([]any) {
			field := raw.(map[string]any)
			for title, old := range f.fields {
				if old["field_id"] == field["field_id"] {
					delete(f.fields, title)
				}
			}
			f.fields[field["field_title"].(string)] = field
		}
		return map[string]any{"result": map[string]any{"errcode": 0}}, nil
	case "add_records":
		f.addRecordCalls++
		body := payload.(map[string]any)
		record := body["records"].([]any)[0].(map[string]any)
		id := fmt.Sprintf("self-record-%d", f.addRecordCalls)
		if !f.hideRecordWrite {
			f.rows = append(f.rows, map[string]any{"record_id": id, "values": record["values"]})
		}
		if f.lostRecordResponse || f.hideRecordWrite {
			return nil, fmt.Errorf("lost response")
		}
		return map[string]any{"result": map[string]any{"errcode": 0, "records": []any{map[string]any{"record_id": id}}}}, nil
	case "add_fields":
		body, _ := payload.(map[string]any)
		definitions, _ := body["fields"].([]map[string]any)
		if len(definitions) != 1 {
			return nil, fmt.Errorf("unexpected field payload: %#v", payload)
		}
		title, _ := definitions[0]["field_title"].(string)
		field := map[string]any{"field_title": title, "field_id": "id-" + title, "field_type": "FIELD_TYPE_TEXT"}
		f.fields[title] = field
		return map[string]any{"result": map[string]any{"fields": []any{field}}}, nil
	default:
		return nil, fmt.Errorf("unexpected operation %s", operation)
	}
}

func bootstrapTestConfig(t *testing.T) (string, config.Config) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	schema := filepath.Join(dir, "schema.json")
	state := filepath.Join(dir, "state.json")
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"test-tenant-route","wecom_operator_userid":"operator-user","registry_document_id":"","registry_key":"test-registry-key","schema_mirror_path":"` + schema + `","state_path":"` + state + `","api_whitelist":{"bootstrap":["get_employee","create_smartsheet","get_doc_auth","get_sheet","get_fields","add_fields","update_fields","delete_fields","get_records","delete_records","add_records"]}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := config.LoadBootstrapCandidate(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, runtime
}

func TestRegistryBootstrapCreatesOncePersistsAndRereads(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	raw, _ := json.Marshal(map[string]string{"owner_authorization": "create_and_persist_default_registry"})
	result, err := server.bootstrapRegistry(context.Background(), runtime, client, raw)
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "created_configured_readback_verified" || output["readback_verified"] != true || client.createCalls != 1 {
		t.Fatalf("result=%#v createCalls=%d", output, client.createCalls)
	}
	admins, _ := client.createBody["admin_users"].([]string)
	if len(admins) != 1 || admins[0] != "operator-user" {
		t.Fatalf("bootstrap create did not bind operator admin: %#v", client.createBody)
	}
	if len(client.fields) != len(registryBootstrapFields) {
		t.Fatalf("field count=%d", len(client.fields))
	}
	persisted, err := config.Load(path)
	if err != nil || persisted.RegistryDocumentID != "registry-created" {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	state, exists, err := loadRegistryBootstrapState(registryBootstrapStatePath(runtime))
	if err != nil || !exists || state.Phase != "verified" || state.DocumentID != "registry-created" {
		t.Fatalf("state=%#v exists=%v err=%v", state, exists, err)
	}
	configured, err := server.bootstrapRegistry(context.Background(), persisted, client, raw)
	if err != nil || configured.(map[string]any)["state"] != "already_configured" || client.createCalls != 1 {
		t.Fatalf("configured=%#v err=%v createCalls=%d", configured, err, client.createCalls)
	}
}

func TestRegistryBootstrapCreatingSentinelStopsDuplicate(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	state := registryBootstrapState{Phase: "creating", StartedAt: "2026-08-10T00:00:00Z", UpdatedAt: "2026-08-10T00:00:00Z"}
	if err := reserveRegistryBootstrapState(registryBootstrapStatePath(runtime), state); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"owner_authorization": "create_and_persist_default_registry"})
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, raw); err == nil {
		t.Fatal("uncertain creating sentinel must fail closed")
	}
	if client.createCalls != 0 {
		t.Fatalf("duplicate create calls=%d", client.createCalls)
	}
}

func TestRegistryBootstrapRejectsPrelockTenantRouteChange(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	release, err := acquireStateFileLock(instanceLifecycleLockPath(runtime))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	raw, _ := json.Marshal(map[string]string{"owner_authorization": "create_and_persist_default_registry"})
	result := make(chan error, 1)
	go func() {
		_, callErr := server.bootstrapRegistry(context.Background(), runtime, client, raw)
		result <- callErr
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "test-tenant-route", "changed-tenant-route", 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "tenant_route") {
		t.Fatalf("prelock tenant route drift was accepted: %v", err)
	}
	if client.createCalls != 0 {
		t.Fatalf("stale prelock client performed create: %d", client.createCalls)
	}
}

func TestRegistryBootstrapRejectsPrelockStatePathChange(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	release, err := acquireStateFileLock(instanceLifecycleLockPath(runtime))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	raw, _ := json.Marshal(map[string]string{"owner_authorization": "create_and_persist_default_registry"})
	result := make(chan error, 1)
	go func() {
		_, callErr := server.bootstrapRegistry(context.Background(), runtime, client, raw)
		result <- callErr
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), runtime.StatePath, runtime.StatePath+"-changed", 1)
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "state_path") {
		t.Fatalf("prelock state path drift was accepted: %v", err)
	}
	if client.createCalls != 0 {
		t.Fatalf("state path drift performed create: %d", client.createCalls)
	}
	if _, err := os.Stat(registryBootstrapStatePath(runtime)); !os.IsNotExist(err) {
		t.Fatalf("state path drift wrote a bootstrap sentinel: %v", err)
	}
}

func TestRegistryBootstrapResumesCreatedDocumentWithoutCreatingAgain(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	state := registryBootstrapState{
		Phase: "created", DocumentID: "registry-resume", ShareURL: "https://example.invalid/resume",
		OperatorDigest: digestValue("operator-user"), StartedAt: "2026-08-10T00:00:00Z", UpdatedAt: "2026-08-10T00:00:01Z",
	}
	if err := reserveRegistryBootstrapState(registryBootstrapStatePath(runtime), state); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"owner_authorization": "create_and_persist_default_registry"})
	result, err := server.bootstrapRegistry(context.Background(), runtime, client, raw)
	if err != nil {
		t.Fatal(err)
	}
	if client.createCalls != 0 || result.(map[string]any)["created"] != false {
		t.Fatalf("result=%#v createCalls=%d", result, client.createCalls)
	}
	persisted, err := config.Load(path)
	if err != nil || persisted.RegistryDocumentID != "registry-resume" {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestRegistryBootstrapRejectsUnverifiedExactEmployeeBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name string
		user map[string]any
		err  error
	}{
		{"provider_denied", map[string]any{"errcode": 60011, "userid": "operator-user", "status": 1}, nil},
		{"wrong_case", map[string]any{"errcode": 0, "userid": "Operator-user", "status": 1}, nil},
		{"inactive", map[string]any{"errcode": 0, "userid": "operator-user", "status": 2}, nil},
		{"missing_status", map[string]any{"errcode": 0, "userid": "operator-user"}, nil},
		{"malformed_status", map[string]any{"errcode": 0, "userid": "operator-user", "status": "1"}, nil},
		{"missing_code", map[string]any{"userid": "operator-user", "status": 1}, nil},
		{"transport", nil, fmt.Errorf("unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, runtime := bootstrapTestConfig(t)
			server := &Server{store: config.NewStore(path)}
			client := &bootstrapFakeClient{employee: test.user, employeeErr: test.err}
			_, err := server.bootstrapRegistry(context.Background(), runtime, client, json.RawMessage(`{"owner_authorization":"create_and_persist_default_registry"}`))
			if err == nil || client.createCalls != 0 {
				t.Fatalf("err=%v creates=%d", err, client.createCalls)
			}
			if _, err := os.Stat(registryBootstrapStatePath(runtime)); !os.IsNotExist(err) {
				t.Fatalf("reserved before verification: %v", err)
			}
		})
	}
}

func TestRegistryBootstrapLegacyDirectoryAllowlistStillWorks(t *testing.T) {
	path, runtime := bootstrapTestConfig(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "get_employee", "list_employees")), 0600); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: config.NewStore(path)}
	client := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	_, err = server.bootstrapRegistry(context.Background(), runtime, client, json.RawMessage(`{"owner_authorization":"create_and_persist_default_registry"}`))
	if err != nil || client.createCalls != 1 {
		t.Fatalf("err=%v creates=%d", err, client.createCalls)
	}
}

func registryDefaultsFixture() *bootstrapFakeClient {
	f := &bootstrapFakeClient{fields: map[string]map[string]any{}}
	for title, template := range registryDefaultFields {
		field := map[string]any{}
		for k, v := range template {
			field[k] = v
		}
		field["field_id"] = "default-" + fmt.Sprint(len(f.fields))
		if title == "文本" {
			field["field_id"] = "default-text"
		}
		f.fields[title] = field
	}
	for _, title := range registryBootstrapFields {
		f.fields[title] = map[string]any{"field_id": "id-" + title, "field_title": title, "field_type": "FIELD_TYPE_TEXT"}
	}
	for i := 0; i < 5; i++ {
		f.rows = append(f.rows, map[string]any{"record_id": fmt.Sprintf("empty-%d", i), "values": map[string]any{}})
	}
	return f
}

func prepareLegacyOwnedRegistry(t *testing.T) (*Server, config.Config) {
	t.Helper()
	path, runtime := bootstrapTestConfig(t)
	store := config.NewStore(path)
	if err := store.PersistRegistryDocumentID("registry-created"); err != nil {
		t.Fatal(err)
	}
	runtime, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	state := registryBootstrapState{Phase: "verified", DocumentID: runtime.RegistryDocumentID, SheetID: "sheet-registry", ShareURL: "https://example.invalid/registry", OperatorDigest: digestValue(runtime.WecomOperatorUserID), StartedAt: "2026-08-10T00:00:00Z", UpdatedAt: "2026-08-10T00:01:00Z"}
	if err := reserveRegistryBootstrapState(registryBootstrapStatePath(runtime), state); err != nil {
		t.Fatal(err)
	}
	return &Server{store: store}, runtime
}

var registryBootstrapAuthorization = json.RawMessage(`{"owner_authorization":"create_and_persist_default_registry"}`)

func TestRegistryBootstrapRepairsLegacyDefaultsAndRegistersSelfOnce(t *testing.T) {
	server, runtime := prepareLegacyOwnedRegistry(t)
	client := registryDefaultsFixture()
	result, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.fields) != 20 || len(client.rows) != 1 || client.addRecordCalls != 1 || client.createCalls != 0 || len(client.deletedFields) != 5 {
		t.Fatalf("incomplete repair fields=%d rows=%d adds=%d creates=%d deletes=%v", len(client.fields), len(client.rows), client.addRecordCalls, client.createCalls, client.deletedFields)
	}
	if client.fields["registry_key"]["field_id"] != "default-text" {
		t.Fatal("primary field was not reused")
	}
	output := result.(map[string]any)
	if output["self_registered"] != true || output["registry_field_count"] != 20 {
		t.Fatalf("output=%v", output)
	}
	before := digestValue(client.rows)
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err != nil {
		t.Fatal(err)
	}
	if before != digestValue(client.rows) || client.addRecordCalls != 1 {
		t.Fatal("idempotent run changed self row or duplicated creation")
	}
	fields := map[string]config.Field{}
	for title, f := range client.fields {
		fields[title] = config.Field{Title: title, ID: f["field_id"].(string), Type: f["field_type"].(string)}
	}
	if doc, count := initializeActiveBusiness(client.rows, fields, runtime.RegistryKey); doc != "" || count != 0 {
		t.Fatal("self record was mistaken for business document")
	}
}

func TestRegistryBootstrapRefusesUnsafeCleanup(t *testing.T) {
	for _, scenario := range []string{"nonempty", "unknown_field", "modified_default", "incomplete", "malformed_record"} {
		t.Run(scenario, func(t *testing.T) {
			server, runtime := prepareLegacyOwnedRegistry(t)
			client := registryDefaultsFixture()
			switch scenario {
			case "nonempty":
				client.rows[0].(map[string]any)["values"] = map[string]any{"default-text": []any{map[string]any{"type": "text", "text": "keep this"}}}
			case "unknown_field":
				client.fields["custom"] = map[string]any{"field_id": "custom", "field_title": "custom", "field_type": "FIELD_TYPE_TEXT"}
			case "modified_default":
				client.fields["数字"]["property_number"] = map[string]any{"decimal_places": 2, "use_separate": true}
			case "incomplete":
				client.incomplete = true
			case "malformed_record":
				delete(client.rows[0].(map[string]any), "values")
			}
			before := digestValue(map[string]any{"fields": client.fields, "rows": client.rows})
			if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if before != digestValue(map[string]any{"fields": client.fields, "rows": client.rows}) || client.addRecordCalls != 0 || client.createCalls != 0 {
				t.Fatal("unsafe snapshot was changed")
			}
		})
	}
}

func TestRegistryBootstrapSelfWriteUncertaintyDoesNotDuplicate(t *testing.T) {
	server, runtime := prepareLegacyOwnedRegistry(t)
	client := registryDefaultsFixture()
	client.hideRecordWrite = true
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
		t.Fatal("invisible write reported success")
	}
	client.hideRecordWrite = false
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
		t.Fatal("unresolved pending write retried")
	}
	if client.addRecordCalls != 1 {
		t.Fatal("duplicated uncertain add_records")
	}
	var pending registrySelfJournal
	raw, err := os.ReadFile(runtime.StatePath + ".registry-self.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	for title, value := range pending.Values {
		values[client.fields[title]["field_id"].(string)] = []any{map[string]any{"type": "text", "text": value}}
	}
	client.rows = []any{map[string]any{"record_id": "recovered-self", "values": values}}
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err != nil {
		t.Fatal(err)
	}
	if client.addRecordCalls != 1 {
		t.Fatal("recovery duplicated write")
	}
}

func TestRegistryBootstrapLostResponseRecoversByReadback(t *testing.T) {
	server, runtime := prepareLegacyOwnedRegistry(t)
	client := registryDefaultsFixture()
	client.lostRecordResponse = true
	if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err != nil {
		t.Fatal(err)
	}
	if client.addRecordCalls != 1 || len(client.rows) != 1 {
		t.Fatal("lost response recovery incomplete")
	}
}

func TestRegistryBootstrapRejectsDuplicateOrConflictingSelf(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			server, runtime := prepareLegacyOwnedRegistry(t)
			client := registryDefaultsFixture()
			if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err != nil {
				t.Fatal(err)
			}
			if duplicate {
				row := client.rows[0].(map[string]any)
				client.rows = append(client.rows, map[string]any{"record_id": "duplicate-self", "values": row["values"]})
			} else {
				client.rows[0].(map[string]any)["values"].(map[string]any)[client.fields["docid"]["field_id"].(string)] = []any{map[string]any{"type": "text", "text": "other-doc"}}
			}
			if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
				t.Fatal("self conflict accepted")
			}
			if client.addRecordCalls != 1 {
				t.Fatal("conflict caused another self write")
			}
		})
	}
}

func TestRegistryBootstrapDoesNotRepairImportedDocument(t *testing.T) {
	path, _ := bootstrapTestConfig(t)
	store := config.NewStore(path)
	if err := store.PersistRegistryDocumentID("imported"); err != nil {
		t.Fatal(err)
	}
	runtime, err := store.Current()
	if err != nil {
		t.Fatal(err)
	}
	client := registryDefaultsFixture()
	before := digestValue(client.fields)
	if _, err := (&Server{store: store}).bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
		t.Fatal("unowned imported registry modified")
	}
	if before != digestValue(client.fields) || client.addRecordCalls != 0 {
		t.Fatal("imported document changed")
	}
}

func TestRegistryBootstrapRejectsChangedSelfMetadata(t *testing.T) {
	for _, title := range registryBootstrapFields {
		t.Run(title, func(t *testing.T) {
			server, runtime := prepareLegacyOwnedRegistry(t)
			client := registryDefaultsFixture()
			if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err != nil {
				t.Fatal(err)
			}
			value := "wrong-nonempty-value"
			if title == "url" {
				value = "https://example.invalid/wrong"
			}
			if title == "created_at" || title == "last_verified_at" || title == "last_change_at" {
				value = "2020-01-01T00:00:00Z"
			}
			client.rows[0].(map[string]any)["values"].(map[string]any)[client.fields[title]["field_id"].(string)] = []any{map[string]any{"type": "text", "text": value}}
			if _, err := server.bootstrapRegistry(context.Background(), runtime, client, registryBootstrapAuthorization); err == nil {
				t.Fatal("changed self metadata accepted")
			}
			if client.addRecordCalls != 1 {
				t.Fatal("conflict caused duplicate self row")
			}
		})
	}
}

// Exercise the actual tools/call entry and managed transport, not only the
// bootstrap helper: previously configured instances skipped client creation.
func TestRegistryBootstrapPublicCallRepairsConfiguredOwnedRegistry(t *testing.T) {
	server, runtime := prepareLegacyOwnedRegistry(t)
	fake := registryDefaultsFixture()
	executorCalls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/gnas/service/getJwtToken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "test-token", "expires_at": time.Now().Add(time.Hour).Unix()}})
			return
		}
		if r.URL.Path != "/gnas/service/wecomExecute" || r.Header.Get("X-GNAS-Managed-Source") != runtime.TenantRoute {
			t.Errorf("unexpected managed route")
			http.Error(w, "unexpected route", http.StatusBadRequest)
			return
		}
		executorCalls++
		upstream, _ := url.Parse(r.Header.Get("X-GNAS-Upstream-Path"))
		operation := ""
		for name, definition := range wecom.Operations {
			if definition.Path == upstream.Path {
				operation = name
				break
			}
		}
		if operation == "get_sheets" {
			operation = "get_sheet"
		}
		body := map[string]any{}
		if operation == "get_employee" {
			body["userid"] = upstream.Query().Get("userid")
		} else {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		for _, key := range []string{"record_ids", "field_ids"} {
			if entries, ok := body[key].([]any); ok {
				ids := []string{}
				for _, entry := range entries {
					ids = append(ids, entry.(string))
				}
				body[key] = ids
			}
		}
		if operation == "add_fields" {
			entries := []map[string]any{}
			for _, entry := range body["fields"].([]any) {
				entries = append(entries, entry.(map[string]any))
			}
			body["fields"] = entries
		}
		response, err := fake.Request(r.Context(), operation, body)
		if err != nil {
			t.Errorf("fake upstream: %v", err)
			http.Error(w, "upstream failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(response["result"])
	}))
	defer endpoint.Close()
	t.Setenv("GNAS_BASE_URL", endpoint.URL)
	t.Setenv("GNAS_APP_ID", "test-app")
	t.Setenv("GNAS_APP_SECRET", "test-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wecom_registry_bootstrap","arguments":{"owner_authorization":"create_and_persist_default_registry"}}}`)
	for attempt := 0; attempt < 2; attempt++ {
		response := server.Handle(context.Background(), request)
		if response == nil || response.Error != nil {
			t.Fatalf("RPC failure: %#v", response)
		}
		result := response.Result.(map[string]any)
		if result["isError"] == true {
			t.Fatalf("tool failed: %#v", result)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["registry_field_count"] != 20 || structured["self_registered"] != true || structured["readback_verified"] != true {
			t.Fatalf("incomplete: %#v", structured)
		}
		if len(fake.fields) != 20 || len(fake.rows) != 1 || fake.addRecordCalls != 1 || fake.createCalls != 0 || fake.fields["registry_key"]["field_id"] != "default-text" {
			t.Fatal("public repair incomplete or duplicated")
		}
	}
	if executorCalls == 0 {
		t.Fatal("configured Registry bypassed managed client")
	}
}
