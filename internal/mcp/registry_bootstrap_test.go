package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type bootstrapFakeClient struct {
	fields      map[string]map[string]any
	createCalls int
	createBody  map[string]any
}

func (f *bootstrapFakeClient) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	switch operation {
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
		items := make([]any, 0, len(f.fields))
		for _, field := range f.fields {
			items = append(items, field)
		}
		return map[string]any{"result": map[string]any{"fields": items}}, nil
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
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"test-tenant-route","wecom_operator_userid":"operator-user","registry_document_id":"","registry_key":"test-registry-key","schema_mirror_path":"` + schema + `","state_path":"` + state + `","api_whitelist":{"bootstrap":["list_employees","create_smartsheet","get_doc_auth","get_sheet","get_fields","add_fields"]}}`
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
	configured, err := server.bootstrapRegistry(context.Background(), persisted, nil, raw)
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
