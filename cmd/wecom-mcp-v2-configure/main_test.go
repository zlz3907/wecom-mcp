package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexBlockIsIdempotentAndBackedUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opt := options{binary: "/tmp/current/bin/wecom-mcp-v2", serviceConfig: "/tmp/zoop.local.json", codexConfig: path}
	first := configureCodex(opt)
	if !first.Configured || first.BackupPath == "" || first.Result != "configured" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), beginMarker) || !strings.Contains(string(data), opt.binary) {
		t.Fatalf("managed block missing: %s", data)
	}
	second := configureCodex(opt)
	if !second.Configured || second.Result != "already_configured" || second.BackupPath != "" {
		t.Fatalf("unexpected second result: %#v", second)
	}
}

func TestTRAERegistrationRejectsConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"zoop_wecom_zhycit":{"command":"/old","args":["--config","/old.json"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	item := configureTRAE(options{binary: "/new", serviceConfig: "/new.json", traeConfig: path})
	if item.Result != "agent_blocked" {
		t.Fatalf("expected conflict block, got %#v", item)
	}
}

func TestTRAERegistrationCreatesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	opt := options{binary: "/tmp/current/bin/wecom-mcp-v2", serviceConfig: "/tmp/zoop.local.json", traeConfig: path}
	first := configureTRAE(opt)
	if !first.Configured || first.Result != "configured" {
		t.Fatalf("unexpected first result: %#v", first)
	}
	second := configureTRAE(opt)
	if !second.Configured || second.Result != "already_configured" {
		t.Fatalf("unexpected second result: %#v", second)
	}
}

func TestAtomicSwitchCurrentReplacesSymlink(t *testing.T) {
	prefix := t.TempDir()
	for _, release := range []string{"old", "new"} {
		if err := os.MkdirAll(filepath.Join(prefix, "releases", release), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicSwitchCurrent(prefix, "old"); err != nil {
		t.Fatal(err)
	}
	if err := atomicSwitchCurrent(prefix, "new"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(prefix, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "releases/new" {
		t.Fatalf("unexpected current target %q", target)
	}
}
