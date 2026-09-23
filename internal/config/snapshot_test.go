package config

import (
	"path/filepath"
	"testing"
)

func TestSnapshotStoreIsIsolatedAndCannotInitialize(t *testing.T) {
	cfg := Config{Version: 1, InstanceName: "existing-name", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", SchemaSource: "z-s00", StatePath: filepath.Join(t.TempDir(), "state.json"), APIWhitelist: map[string][]string{"read": {"get_records"}}}
	store, err := NewSnapshotStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIWhitelist["read"][0] = "add_records"
	first, err := store.Current()
	if err != nil || first.Allows("add_records") {
		t.Fatal("caller mutated the stored snapshot")
	}
	first.APIWhitelist["read"][0] = "del_records"
	second, err := store.Current()
	if err != nil || !second.Allows("get_records") {
		t.Fatal("returned config mutated the stored snapshot")
	}
	if _, err := store.BootstrapCandidate(); err == nil {
		t.Fatal("snapshot bootstrap accepted")
	}
	if err := store.PersistRegistryDocumentID("registry-a"); err == nil {
		t.Fatal("snapshot persistence accepted")
	}
	if _, err := store.CommitInitialized(InitializationCommit{}); err == nil {
		t.Fatal("snapshot initialization accepted")
	}
}
