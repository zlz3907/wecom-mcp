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
