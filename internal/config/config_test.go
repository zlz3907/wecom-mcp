package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAndAllow(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.md")
	if err := os.WriteFile(schema, []byte("# schema"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "instance.json")
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"wecom-zhycit-admin-assistant","registry_document_id":"registry1","registry_key":"zhycit_zoop_governance_v2","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"read":["get_records"]}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Allows("get_records") || got.Allows("add_records") {
		t.Fatalf("white list not enforced: %#v", got)
	}
}

func TestAllowlistGroupDoesNotGrantAnotherCapability(t *testing.T) {
	runtime := Config{APIWhitelist: map[string][]string{
		"schema_migration": {"add_fields"},
		"records":          {"add_records"},
	}}
	if !runtime.Allows("add_fields") || !runtime.AllowsInGroup("schema_migration", "add_fields") {
		t.Fatal("schema migration group must allow its own operation")
	}
	if runtime.AllowsInGroup("records", "add_fields") {
		t.Fatal("an operation in another group must not grant record capability")
	}
}

func TestStoreReloadsChangedWhitelistWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.md")
	if err := os.WriteFile(schema, []byte("# schema"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "instance.json")
	writeConfig := func(operations string) {
		data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"wecom-zhycit-admin-assistant","registry_document_id":"registry1","registry_key":"zhycit_zoop_governance_v2","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"managed":[` + operations + `]}}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`"get_records"`)
	store := NewStore(path)
	first, err := store.Current()
	if err != nil || !first.Allows("get_records") || first.Allows("add_records") {
		t.Fatalf("initial allowlist=%#v err=%v", first, err)
	}
	writeConfig(`"add_records"`)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	second, err := store.Current()
	if err != nil || second.Allows("get_records") || !second.Allows("add_records") {
		t.Fatalf("reloaded allowlist=%#v err=%v", second, err)
	}
}

func TestBootstrapCandidateAllowsOnlyMissingRegistryDocumentID(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.json")
	path := filepath.Join(dir, "instance.json")
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"wecom-zhycit-admin-assistant","registry_document_id":"","registry_key":"zhycit_zoop_governance_v2","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"bootstrap":["create_smartsheet","get_sheet","get_fields","add_fields"]}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("normal runtime must reject an empty registry_document_id")
	}
	candidate, err := LoadBootstrapCandidate(path)
	if err != nil || candidate.RegistryDocumentID != "" {
		t.Fatalf("bootstrap candidate=%#v err=%v", candidate, err)
	}
	store := NewStore(path)
	if err := store.PersistRegistryDocumentID("registry-created"); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Current()
	if err != nil || persisted.RegistryDocumentID != "registry-created" {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if err := store.PersistRegistryDocumentID("registry-other"); err == nil {
		t.Fatal("must not overwrite a different registry_document_id")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config mode=%v err=%v", info.Mode().Perm(), err)
	}
}
