package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"test-tenant-route","registry_document_id":"registry1","registry_key":"test-registry-key","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"read":["get_records"]}}`
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

func TestSchemaAdminIdentityAcceptsCanonicalWindowsPrincipalOnly(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "zhyc", want: true},
		{value: `DESKTOP-123\zhyc`, want: true},
		{value: `CORP.EXAMPLE\zhyc`, want: true},
		{value: `CORP\`, want: false},
		{value: `\zhyc`, want: false},
		{value: `CORP\team\zhyc`, want: false},
		{value: `CORP/zhyc`, want: false},
	} {
		if got := schemaAdminIdentity.MatchString(test.value); got != test.want {
			t.Fatalf("schemaAdminIdentity.MatchString(%q)=%v, want %v", test.value, got, test.want)
		}
	}
}

func TestCommitInitializedBacksUpAndAtomicallySwitchesGeneration(t *testing.T) {
	const secretCanary = "SECRET-CANARY-DO-NOT-PERSIST"
	t.Setenv("GNAS_APP_SECRET", secretCanary)
	directory := t.TempDir()
	configPath := filepath.Join(directory, "instance.json")
	oldSchemaPath := filepath.Join(directory, "old-schema.json")
	newSchemaPath := filepath.Join(directory, "generations", "new-schema.json")
	fields := map[string][]Field{}
	for index := 1; index <= 9; index++ {
		role := "Z-S0" + string(rune('0'+index))
		fields[role] = []Field{{Title: "标题", ID: "field" + string(rune('0'+index)), Type: "FIELD_TYPE_TEXT"}}
	}
	if err := WriteOnlineMirror(oldSchemaPath, fields, "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := WriteOnlineMirror(newSchemaPath, fields, "2026-08-27T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	value := Config{
		Version: 1, InstanceName: "zoop_wecom_zhycit", TenantRoute: "tenant-route", RegistryKey: "registry-key",
		SchemaMirrorPath: oldSchemaPath, StatePath: filepath.Join(directory, "state.json"),
		APIWhitelist: map[string][]string{"instance_initialize": {"get_sheet", "get_fields", "get_records"}},
	}
	data, _ := json.Marshal(value)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	newSchema, err := LoadSchema(newSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	backupPath, err := NewStore(configPath).CommitInitialized(InitializationCommit{RegistryDocumentID: "registry-doc", RegistrySheetID: "registry-sheet", SchemaMirrorPath: newSchemaPath, SchemaVersion: "zoop-v1", SchemaDigest: newSchema.Digest, InitializationGeneration: newSchema.Digest})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RegistryDocumentID != "registry-doc" || loaded.RegistrySheetID != "registry-sheet" || loaded.SchemaMirrorPath != newSchemaPath || loaded.SchemaDigest != newSchema.Digest || loaded.InitializedState != "config_committed" {
		t.Fatalf("config did not switch generations: %#v", loaded)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backup), oldSchemaPath) || strings.Contains(string(backup), "registry-doc") {
		t.Fatalf("backup is not the original complete config: %s", backup)
	}
	currentBytes, _ := os.ReadFile(configPath)
	if strings.Contains(string(backup), secretCanary) || strings.Contains(string(currentBytes), secretCanary) {
		t.Fatal("environment secret leaked into config commit or backup")
	}
	if info, _ := os.Stat(backupPath); info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode=%o", info.Mode().Perm())
	}
}

func TestCommitInitializedFailureLeavesConfigUntouched(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "instance.json")
	value := Config{
		Version: 1, InstanceName: "zoop_wecom_zhycit", TenantRoute: "tenant-route", RegistryKey: "registry-key",
		SchemaMirrorPath: filepath.Join(directory, "old-schema.json"), StatePath: filepath.Join(directory, "state.json"),
		APIWhitelist: map[string][]string{"instance_initialize": {"get_sheet", "get_fields", "get_records"}},
	}
	data, _ := json.Marshal(value)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(configPath).CommitInitialized(InitializationCommit{RegistryDocumentID: "registry-doc", RegistrySheetID: "registry-sheet", SchemaMirrorPath: filepath.Join(directory, "missing-schema.json"), SchemaVersion: "zoop-v1", SchemaDigest: strings.Repeat("a", 64), InitializationGeneration: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("missing generated schema must fail")
	}
	after, _ := os.ReadFile(configPath)
	if string(after) != string(data) {
		t.Fatal("failed commit changed original config")
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

func TestConfigDigestCoversStatePathAndCapabilityGroups(t *testing.T) {
	base := Config{Version: 1, InstanceName: "instance", TenantRoute: "route", RegistryDocumentID: "registry", RegistryKey: "key", SchemaMirrorPath: "/tmp/schema.json", StatePath: "/tmp/state-a.json", APIWhitelist: map[string][]string{"instance_initialize": {"get_sheet"}}}
	changedState := base
	changedState.StatePath = "/tmp/state-b.json"
	changedGroup := base
	changedGroup.APIWhitelist = map[string][]string{"other": {"get_sheet"}}
	if base.Digest() == changedState.Digest() || base.Digest() == changedGroup.Digest() {
		t.Fatal("config digest omitted state path or capability group")
	}
}

func TestConfigValidatesAndDigestsWeComOperatorUserID(t *testing.T) {
	base := Config{Version: 1, InstanceName: "instance", TenantRoute: "route", RegistryDocumentID: "registry", RegistryKey: "key", SchemaMirrorPath: "/tmp/schema.json", StatePath: "/tmp/state.json", APIWhitelist: map[string][]string{"instance_initialize": {"get_sheet"}}}
	legacy := base
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy config must still load: %v", err)
	}
	base.WecomOperatorUserID = "Operator.User-01@example"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.WecomOperatorUserID = "Operator.User-02@example"
	if base.Digest() == changed.Digest() {
		t.Fatal("config digest omitted wecom_operator_userid")
	}
	for _, invalid := range []string{"operator user", "用户", strings.Repeat("a", 65)} {
		candidate := base
		candidate.WecomOperatorUserID = invalid
		if candidate.Validate() == nil {
			t.Fatalf("invalid wecom userid accepted: %q", invalid)
		}
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
		data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"test-tenant-route","registry_document_id":"registry1","registry_key":"test-registry-key","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"managed":[` + operations + `]}}`
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
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"test-tenant-route","registry_document_id":"","registry_key":"test-registry-key","schema_mirror_path":"` + schema + `","state_path":"` + filepath.Join(dir, "state.json") + `","api_whitelist":{"bootstrap":["create_smartsheet","get_sheet","get_fields","add_fields"]}}`
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

func TestConfigRejectsUnknownSecretLikeFields(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "instance.json")
	data := `{"version":1,"instance_name":"zoop_wecom_zhycit","tenant_route":"tenant-route","registry_document_id":"registry","registry_key":"registry-key","schema_mirror_path":"` + filepath.Join(directory, "schema.json") + `","state_path":"` + filepath.Join(directory, "state.json") + `","api_whitelist":{"instance_initialize":["get_sheet"]},"GNAS_APP_SECRET":"SECRET-CANARY"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("secret-like unknown config field must fail closed: %v", err)
	}
}
