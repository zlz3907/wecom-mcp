package team

import (
	"context"
	"io"
	"log/slog"
	"testing"

	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

func TestOAuthCallRejectsMissingOrCrossInstanceEmployee(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.AuthorizationTenant = "tenant-one"
	cfg.AuthorizationResource = "resource-one"
	s, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	d := legacymcp.ToolDefinition{Name: "wecom_record_apply", Access: legacymcp.ToolAccessOperator}
	for _, tc := range []struct {
		name     string
		decision AuthorizationDecision
	}{
		{"missing employee", AuthorizationDecision{Active: true, Tenant: "tenant-one", Resource: "resource-one"}},
		{"other tenant", AuthorizationDecision{Active: true, Tenant: "tenant-two", Resource: "resource-one", UserID: "employee-one"}},
		{"other resource", AuthorizationDecision{Active: true, Tenant: "tenant-one", Resource: "resource-two", UserID: "employee-one"}},
		{"inactive", AuthorizationDecision{Tenant: "tenant-one", Resource: "resource-one", UserID: "employee-one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), requestAuthorizationKey{}, requestAuthorization{decision: tc.decision, tools: map[string]bool{d.Name: true}})
			result, err := s.callTool(ctx, nil, RolePolicy, d)
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOAuthCannotDisableEmployeePolicy(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = false
	if _, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("OAuth allowed without employee policy")
	}
}
