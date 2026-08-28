package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

func TestResolveAuthorizationV1FixtureAndHTTPAdapter(t *testing.T) {
	fixture, err := os.ReadFile("testdata/resolve_authorization_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer adapter-test" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var query AuthorizationQuery
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Fatal(err)
		}
		if query != (AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop"}) {
			t.Fatalf("query=%#v", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	resolver := &HTTPAuthorizationResolver{
		Endpoint: server.URL,
		Client:   server.Client(),
		Authorize: func(request *http.Request) error {
			request.Header.Set("Authorization", "Bearer adapter-test")
			return nil
		},
	}
	query := AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop"}
	decision, err := resolver.ResolveAuthorization(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Resource != "gmzoop" || decision.PolicyVersion != "policy-test-v7" || !decision.Active {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestHTTPAuthorizationResolverRejectsStructuralDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema_version":"1","tenant":"tenant-test","userid":"CaseSensitiveUserID","active":true,"effective_scopes":[],"effective_tools":[],"policy_version":"v1","evaluated_at":"2026-08-29T08:00:00Z","unexpected":true}`)
	}))
	defer server.Close()
	resolver := &HTTPAuthorizationResolver{Endpoint: server.URL, Client: server.Client(), Authorize: func(*http.Request) error { return nil }}
	_, err := resolver.ResolveAuthorization(context.Background(), AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop"})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err=%v", err)
	}
}

func TestHTTPAuthorizationResolverHonorsContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer server.Close()
	resolver := &HTTPAuthorizationResolver{Endpoint: server.URL, Client: server.Client(), Authorize: func(*http.Request) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := resolver.ResolveAuthorization(ctx, AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop"})
	if err == nil {
		t.Fatal("resolver timeout must fail closed")
	}
}

func TestCachedAuthorizationResolverNeverServesExpiredDecisionOnFailure(t *testing.T) {
	upstream := &sequenceAuthorizationResolver{decisions: []AuthorizationDecision{testAuthorizationDecision("*")}, errors: []error{nil, errors.New("upstream unavailable")}}
	cache, err := NewCachedAuthorizationResolver(upstream, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	query := AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop"}
	if _, err := cache.ResolveAuthorization(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.ResolveAuthorization(context.Background(), query); err != nil || upstream.calls != 1 {
		t.Fatalf("cache miss: calls=%d err=%v", upstream.calls, err)
	}
	now = now.Add(2 * time.Second)
	if _, err := cache.ResolveAuthorization(context.Background(), query); err == nil || upstream.calls != 2 {
		t.Fatalf("stale authorization served: calls=%d err=%v", upstream.calls, err)
	}
}

func TestAuthorizedToolSetExpandsWildcardOnlyToCurrentCatalog(t *testing.T) {
	definitions, err := testToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	decision := testAuthorizationDecision("*")
	tools, err := authorizedToolSet(decision, definitions, []string{"wecom.mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != len(definitions) || !tools["wecom_record_query"] || !tools["wecom_record_apply"] {
		t.Fatalf("tools=%#v", tools)
	}
	decision.EffectiveTools = []string{"future_tool_not_in_this_release"}
	if _, err := authorizedToolSet(decision, definitions, nil); err == nil {
		t.Fatal("unknown future tool must fail closed")
	}
}

func TestUserAuthorizationFiltersToolsAndPreservesRoleBoundary(t *testing.T) {
	resolver := &staticAuthorizationResolver{decision: testAuthorizationDecision("wecom_record_query", "wecom_record_apply")}
	server := newUserAuthorizationHTTPServer(t, RoleReader, resolver, io.Discard)
	defer server.Close()
	tools := listTools(t, server.URL, "reader")
	if !tools["wecom_record_query"] || tools["wecom_record_apply"] {
		t.Fatalf("reader tools=%#v", tools)
	}
}

func TestUserAuthorizationWildcardIsStillRoleBounded(t *testing.T) {
	resolver := &staticAuthorizationResolver{decision: testAuthorizationDecision("*")}
	readerServer := newUserAuthorizationHTTPServer(t, RoleReader, resolver, io.Discard)
	defer readerServer.Close()
	reader := listTools(t, readerServer.URL, "reader")
	if !reader["wecom_record_query"] || reader["wecom_record_apply"] || reader["wecom_registry_bootstrap"] {
		t.Fatalf("reader wildcard tools=%#v", reader)
	}
	adminServer := newUserAuthorizationHTTPServer(t, RoleAdmin, resolver, io.Discard)
	defer adminServer.Close()
	admin := listTools(t, adminServer.URL, "admin")
	if !admin["wecom_record_apply"] || !admin["wecom_registry_bootstrap"] {
		t.Fatalf("admin wildcard tools=%#v", admin)
	}
}

func TestCallToolRechecksAuthorizationContext(t *testing.T) {
	cfg := testUserAuthorizationConfig(t)
	service, err := NewServiceWithAuthorizationResolver(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), &staticAuthorizationResolver{decision: testAuthorizationDecision("wecom_record_query")})
	if err != nil {
		t.Fatal(err)
	}
	var writeDefinition legacymcp.ToolDefinition
	for _, definition := range service.definitions {
		if definition.Name == "wecom_record_apply" {
			writeDefinition = definition
			break
		}
	}
	if writeDefinition.Name == "" {
		t.Fatal("write tool definition missing")
	}
	decision := testAuthorizationDecision("wecom_record_query")
	ctx := context.WithValue(context.Background(), requestAuthorizationKey{}, requestAuthorization{
		decision: decision,
		tools:    map[string]bool{"wecom_record_query": true},
	})
	result, callErr := service.callTool(ctx, nil, RoleAdmin, writeDefinition)
	if callErr != nil || result == nil || !result.IsError {
		t.Fatalf("callErr=%v result=%#v", callErr, result)
	}
	content, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(content.Text, "user policy") {
		t.Fatalf("result=%#v", result)
	}
}

func TestUserAuthorizationFailsClosedForInactiveMissingScopeAndResolverError(t *testing.T) {
	tests := map[string]*staticAuthorizationResolver{
		"inactive": {decision: func() AuthorizationDecision {
			value := testAuthorizationDecision("*")
			value.Active = false
			return value
		}()},
		"missing scope": {decision: func() AuthorizationDecision {
			value := testAuthorizationDecision("*")
			value.EffectiveScopes = nil
			return value
		}()},
		"resolver error": {err: errors.New("timeout")},
	}
	for name, resolver := range tests {
		t.Run(name, func(t *testing.T) {
			server := newUserAuthorizationHTTPServer(t, RoleReader, resolver, io.Discard)
			defer server.Close()
			response := postRPC(t, server.URL+"/mcp", "reader", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d", response.StatusCode)
			}
		})
	}
}

func TestUserAuthorizationRequiresMappedWeComUserIDAndAuditsWithoutPII(t *testing.T) {
	var audit bytes.Buffer
	resolver := &staticAuthorizationResolver{decision: testAuthorizationDecision("wecom_record_query")}
	cfg := testUserAuthorizationConfig(t)
	service, err := NewServiceWithAuthorizationResolver(cfg, slog.New(slog.NewJSONHandler(&audit, nil)), resolver)
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		extra := map[string]any{"role": string(RoleReader), "issuer": "https://login.example.test"}
		if token != "missing-userid" {
			extra["wecom_userid"] = "CaseSensitiveUserID"
		}
		return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Hour), UserID: "opaque-subject", Scopes: []string{"wecom.mcp"}, Extra: extra}, nil
	}
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()

	denied := postRPC(t, server.URL+"/mcp", "missing-userid", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("missing userid status=%d", denied.StatusCode)
	}
	allowed := postRPC(t, server.URL+"/mcp", "reader", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("allowed status=%d", allowed.StatusCode)
	}
	text := audit.String()
	if !strings.Contains(text, `"policy_version":"policy-test-v7"`) || !strings.Contains(text, `"subject_hash":"`) {
		t.Fatalf("audit=%s", text)
	}
	for _, forbidden := range []string{"CaseSensitiveUserID", "opaque-subject", "reader"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("audit leaked %q: %s", forbidden, text)
		}
	}
}

type staticAuthorizationResolver struct {
	decision AuthorizationDecision
	err      error
}

func (r *staticAuthorizationResolver) ResolveAuthorization(_ context.Context, query AuthorizationQuery) (AuthorizationDecision, error) {
	if r.err != nil {
		return AuthorizationDecision{}, r.err
	}
	decision := r.decision
	decision.Tenant = query.Tenant
	decision.UserID = query.UserID
	decision.Resource = query.Resource
	return decision, nil
}

type sequenceAuthorizationResolver struct {
	mu        sync.Mutex
	decisions []AuthorizationDecision
	errors    []error
	calls     int
}

func (r *sequenceAuthorizationResolver) ResolveAuthorization(_ context.Context, query AuthorizationQuery) (AuthorizationDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.calls
	r.calls++
	if index < len(r.errors) && r.errors[index] != nil {
		return AuthorizationDecision{}, r.errors[index]
	}
	decision := r.decisions[min(index, len(r.decisions)-1)]
	decision.Tenant, decision.UserID, decision.Resource = query.Tenant, query.UserID, query.Resource
	return decision, nil
}

func testAuthorizationDecision(tools ...string) AuthorizationDecision {
	return AuthorizationDecision{
		SchemaVersion: resolveAuthorizationSchemaV1,
		Tenant:        "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop", Active: true,
		EffectiveScopes: []string{"wecom.mcp"}, EffectiveTools: tools,
		PolicyVersion: "policy-test-v7", EvaluatedAt: time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC),
	}
}

func testUserAuthorizationConfig(t *testing.T) Config {
	cfg := testServiceConfig(t)
	cfg.RequiredScopes = []string{"wecom.mcp"}
	cfg.UserAuthorizationEnabled = true
	cfg.AuthorizationTenant = "tenant-test"
	cfg.AuthorizationResource = "gmzoop"
	cfg.AuthorizationCacheTTL = time.Nanosecond
	cfg.AuthorizationTimeout = time.Second
	return cfg
}

func newUserAuthorizationHTTPServer(t *testing.T, role Role, resolver AuthorizationResolver, audit io.Writer) *httptest.Server {
	t.Helper()
	cfg := testUserAuthorizationConfig(t)
	service, err := NewServiceWithAuthorizationResolver(cfg, slog.New(slog.NewJSONHandler(audit, nil)), resolver)
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, _ string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour), UserID: "opaque-subject", Scopes: []string{"wecom.mcp"},
			Extra: map[string]any{"role": string(role), "issuer": "https://login.example.test", "wecom_userid": "CaseSensitiveUserID"},
		}, nil
	}
	return httptest.NewServer(service.Handler(verifier))
}

func testToolDefinitions() ([]legacymcp.ToolDefinition, error) {
	return legacymcp.ToolDefinitions()
}
