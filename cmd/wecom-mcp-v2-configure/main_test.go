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

func TestTRAEWorkRegistrationUsesRequestedClientName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	item := configureTRAE(options{client: "trae-work-cn", binary: "/tmp/wecom-mcp-v2", serviceConfig: "/tmp/zoop.local.json", traeConfig: path})
	if !item.Configured || item.Result != "configured" || item.Client != "trae-work-cn" {
		t.Fatalf("unexpected TRAE Work CN result: %#v", item)
	}
}

func TestDefaultTRAEConfigPathUsesWindowsAppData(t *testing.T) {
	got := defaultTRAEConfigPath(`C:\Users\test`, `C:\Users\test\AppData\Roaming`, "windows")
	want := filepath.Join(`C:\Users\test\AppData\Roaming`, "TRAE SOLO CN", "User", "mcp.json")
	if got != want {
		t.Fatalf("default TRAE config path=%q want=%q", got, want)
	}
}

func TestDefaultServiceConfigPathUsesManagedPerUserLocation(t *testing.T) {
	windows := defaultServiceConfigPath(
		`C:\Users\test`,
		`C:\Users\test\AppData\Local`,
		"",
		"windows",
	)
	windowsWant := filepath.Join(`C:\Users\test\AppData\Local`, "wecom-mcp-v2", "config", "zoop_wecom_zhycit.local.json")
	if windows != windowsWant {
		t.Fatalf("Windows service config path=%q want=%q", windows, windowsWant)
	}

	posix := defaultServiceConfigPath("/Users/test", "", "/private/config", "darwin")
	posixWant := filepath.Join("/private/config", "wecom-mcp-v2", "zoop_wecom_zhycit.local.json")
	if posix != posixWant {
		t.Fatalf("POSIX service config path=%q want=%q", posix, posixWant)
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

func TestConfigTargetRejectsSymlinkAndWorldWritableParent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.toml")
	actual := filepath.Join(dir, "actual.toml")
	if err := os.WriteFile(actual, []byte("model = \"gpt\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, target); err != nil {
		t.Fatal(err)
	}
	if err := validateConfigTarget(target, "--codex-config", true); err == nil {
		t.Fatal("expected symlink to be rejected")
	}

	missing := filepath.Join(dir, "missing", "config.toml")
	if err := os.Mkdir(filepath.Dir(missing), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(missing), 0777); err != nil {
		t.Fatal(err)
	}
	if err := validateConfigTarget(missing, "--codex-config", true); err == nil {
		t.Fatal("expected world-writable parent to be rejected")
	}
}

func TestBackupDoesNotOverwriteSameSecondBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opt := options{binary: "/tmp/current/bin/wecom-mcp-v2", serviceConfig: "/tmp/zoop.local.json", codexConfig: path}
	first := configureCodex(opt)
	if !first.Configured {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := configureCodex(opt)
	if !second.Configured || second.BackupPath == first.BackupPath {
		t.Fatalf("expected unique backup paths, first=%#v second=%#v", first, second)
	}
}
