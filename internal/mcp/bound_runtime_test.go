package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestBoundServerRejectsDriftAfterTransportPreflight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	runtime := config.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "operator-a", AIExecutionSubjectRecordID: "executor-a", APIWhitelist: map[string][]string{"read": {"get_records"}, "app_message": {"list_employees", "send_app_message"}}}
	write := func(c config.Config) {
		t.Helper()
		b, _ := json.Marshal(c)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(runtime)
	server, err := NewWithBoundConfig(path, runtime)
	if err != nil {
		t.Fatal(err)
	}
	// The transport has already authenticated and checked the binding generation.
	if _, err := server.store.Current(); err != nil {
		t.Fatal(err)
	}
	changed := runtime
	changed.TenantRoute = "source-b"
	write(changed)
	for _, call := range []struct {
		name string
		args string
	}{
		{"wecom_record_query", `{"target_role":"Z-S01"}`},
		{"wecom_send_app_message", `{"recipient_userid":"test-member","text":"not sent","idempotency_key":"test-key-no-send-0001"}`},
		{"wecom_instance_initialize_status", `{}`},
		{"wecom_schema_migration_preview", `{}`},
	} {
		_, err := server.CallToolWithOAuthEmployee(context.Background(), call.name, json.RawMessage(call.args), "test-employee")
		if err == nil || !strings.Contains(err.Error(), "protected instance changed") {
			t.Fatalf("%s bypassed actual-read guard: %v", call.name, err)
		}
	}
}

func TestOAuthInitializerRejectsDifferentConfiguredOperator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	runtime := config.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "operator-b", AIExecutionSubjectRecordID: "executor-a", APIWhitelist: map[string][]string{"read": {"get_records"}}}
	data, _ := json.Marshal(runtime)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	bound, err := NewWithBoundConfig(path, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for label, server := range map[string]*Server{"fixed": New(path), "bound": bound} {
		for _, tool := range []string{"wecom_instance_initialize", "wecom_instance_initialize_status", "wecom_instance_initialize_apply"} {
			_, err := server.CallToolWithOAuthEmployee(context.Background(), tool, json.RawMessage(`{}`), "operator-a")
			if err == nil || !strings.Contains(err.Error(), "操作人与当前登录用户不一致") {
				t.Fatalf("%s %s: expected identity mismatch before any remote call, got %v", label, tool, err)
			}
		}
	}
	ctx := context.WithValue(context.Background(), oauthInitializerActorKey{}, "operator-a")
	runtime.WecomOperatorUserID = "operator-a"
	if err := verifyOAuthInitializerActor(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	// Apply reloads the configuration under the lifecycle lock and must reject
	// a changed operator before observing or executing any remote operations.
	runtime.WecomOperatorUserID = "operator-b"
	if err := verifyOAuthInitializerActor(ctx, runtime); err == nil {
		t.Fatal("changed locked operator was accepted")
	}
}

func TestOAuthInitializerWithoutRegistryReachesOwnerGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	runtime := config.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "operator-a", APIWhitelist: map[string][]string{"read": {"get_records"}}}
	data, _ := json.Marshal(runtime)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct{ tool, arguments string }{
		{"wecom_instance_initialize", `{"action":"apply"}`},
		{"wecom_instance_initialize_apply", `{}`},
	} {
		_, err := New(path).CallToolWithOAuthEmployee(context.Background(), call.tool, json.RawMessage(call.arguments), "operator-a")
		if err == nil || !strings.Contains(err.Error(), "缺少实例初始化固定 Owner 授权") {
			t.Fatalf("%s did not reach initializer authorization: %v", call.tool, err)
		}
	}
}
