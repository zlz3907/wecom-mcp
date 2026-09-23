package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundStoreRejectsSameTimestampReplacement(t *testing.T) {
	for _, field := range []string{"source", "registry", "state", "operator", "executor", "allowlist"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "instance.json")
			cfg := Config{Version: 1, InstanceName: "local-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), APIWhitelist: map[string][]string{"read": {"get_records"}}}
			write := func(c Config) {
				b, _ := json.Marshal(c)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(cfg)
			before, _ := os.Stat(path)
			store, err := NewBoundStore(path, cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Mutating a returned map must not change the pinned expected configuration.
			got, _ := store.Current()
			got.APIWhitelist["read"][0] = "delete_records"
			if got, err = store.Current(); err != nil || !got.Allows("get_records") {
				t.Fatal("bound store leaked a mutable alias")
			}
			bad := cloneConfig(cfg)
			switch field {
			case "source":
				bad.TenantRoute = "source-b"
			case "registry":
				bad.RegistryDocumentID = "registry-b"
			case "state":
				bad.StatePath = filepath.Join(dir, "other.json")
			case "operator":
				bad.WecomOperatorUserID = "other"
			case "executor":
				bad.AIExecutionSubjectRecordID = "other"
			case "allowlist":
				bad.APIWhitelist["read"] = []string{"delete_records"}
			}
			write(bad)
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Current(); err == nil {
				t.Fatal("replaced config accepted")
			}
			if _, err := store.BootstrapCandidate(); err == nil {
				t.Fatal("bootstrap bypassed binding")
			}
			if err := store.PersistRegistryDocumentID(cfg.RegistryDocumentID); err == nil {
				t.Fatal("writeback bypassed binding")
			}
			write(cfg)
			if _, err := store.Current(); err != nil {
				t.Fatal("same generation did not recover", err)
			}
		})
	}
}

func TestBoundStoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	alias := filepath.Join(dir, "alias.json")
	cfg := Config{Version: 1, InstanceName: "a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), APIWhitelist: map[string][]string{"read": {"get_records"}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(real, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := NewBoundStore(alias, cfg); err == nil {
		t.Fatal("symlink accepted")
	}
}
