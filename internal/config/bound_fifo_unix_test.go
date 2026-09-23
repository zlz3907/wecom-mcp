//go:build !windows

package config

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBoundStoreRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, InstanceName: "a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), APIWhitelist: map[string][]string{"read": {"get_records"}}}
	done := make(chan error, 1)
	go func() { _, err := NewBoundStore(path, cfg); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked")
	}
}
