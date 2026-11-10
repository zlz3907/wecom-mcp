package team

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// A managed and an unmanaged authorization can resolve to exactly the same
// tool set. The cached server must still use each request's policy mode.
func TestToolCallProtocolAcrossCachedPolicyModes(t *testing.T) {
	for _, managedFirst := range []bool{false, true} {
		name := "unmanaged-first"
		if managedFirst {
			name = "managed-first"
		}
		t.Run(name, func(t *testing.T) {
			decision := testAuthorizationDecision("wecom_schema_status", "wecom_employee_list")
			resolver := &staticAuthorizationResolver{decision: decision}
			service, err := NewServiceWithAuthorizationResolver(testUserAuthorizationConfig(t), slog.New(slog.NewTextHandler(io.Discard, nil)), resolver)
			if err != nil {
				t.Fatal(err)
			}
			verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
				return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: "fixture", Scopes: []string{"wecom.mcp"}, Extra: map[string]any{"role": string(RoleReader), "wecom_userid": "fixture-member", "gnas_principal_assertion": "fixture-assertion"}}, nil
			}
			server := httptest.NewServer(service.Handler(verifier))
			defer server.Close()
			type rpcResponse struct {
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
				Result *struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			call := func(tool string) rpcResponse {
				t.Helper()
				request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{}}})
				if err != nil {
					t.Fatal(err)
				}
				response := postRPC(t, server.URL+"/mcp", "fixture", string(request))
				defer response.Body.Close()
				var body rpcResponse
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatalf("decode %q HTTP %d: %v", tool, response.StatusCode, err)
				}
				return body
			}
			var responses [2]map[string]rpcResponse
			for i, managed := range []bool{managedFirst, !managedFirst} {
				resolver.decision.ManagedToolPolicy = managed
				tools := listTools(t, server.URL, "fixture")
				if len(tools) != 2 || !tools["wecom_schema_status"] || !tools["wecom_employee_list"] {
					t.Fatalf("managed=%v tools=%v", managed, tools)
				}
				responses[i] = make(map[string]rpcResponse)
				for _, tool := range []string{"wecom_record_query", "unknown_synthetic_tool", "", "invalid tool"} {
					responses[i][tool] = call(tool)
				}
			}
			if len(service.servers) != 1 {
				t.Fatalf("expected one reused server, got %d", len(service.servers))
			}
			for i, managed := range []bool{managedFirst, !managedFirst} {
				for tool, response := range responses[i] {
					if managed && (tool == "wecom_record_query" || tool == "unknown_synthetic_tool") {
						if response.Error != nil || response.Result == nil || !response.Result.IsError {
							t.Fatalf("managed tool %q: expected normal tool error, got %+v", tool, response)
						}
					} else if response.Error == nil || response.Result != nil {
						t.Fatalf("managed=%v tool %q: expected SDK protocol error, got %+v", managed, tool, response)
					}
				}
			}
			for _, tool := range []string{"", "invalid tool"} {
				if responses[0][tool].Error.Code != responses[1][tool].Error.Code {
					t.Fatalf("tool %q protocol error changed across policy modes", tool)
				}
			}
		})
	}
}
