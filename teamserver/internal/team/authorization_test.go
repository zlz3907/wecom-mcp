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
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer adapter-test" || r.Header.Get("X-Auth-Type") != "service_jwt" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var query AuthorizationQuery
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Fatal(err)
		}
		if query != testAuthorizationQuery() {
			t.Fatalf("query=%#v", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	resolver := &HTTPAuthorizationResolver{
		Endpoint: server.URL, Client: server.Client(), TokenProvider: staticServiceJWTProvider("adapter-test"),
	}
	query := testAuthorizationQuery()
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
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"data":{"schema_version":1,"tenant":"tenant-test","userid":"CaseSensitiveUserID","resource":"gmzoop","active":true,"effective_scopes":[],"effective_tools":[],"policy_version":"v1","evaluated_at":"2026-08-29T08:00:00Z","unexpected":true}}`)
	}))
	defer server.Close()
	resolver := &HTTPAuthorizationResolver{Endpoint: server.URL, Client: server.Client(), TokenProvider: staticServiceJWTProvider("adapter-test")}
	_, err := resolver.ResolveAuthorization(context.Background(), testAuthorizationQuery())
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
	resolver := &HTTPAuthorizationResolver{Endpoint: server.URL, Client: server.Client(), TokenProvider: staticServiceJWTProvider("adapter-test")}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := resolver.ResolveAuthorization(ctx, testAuthorizationQuery())
	if err == nil {
		t.Fatal("resolver timeout must fail closed")
	}
}

func TestGNASServiceJWTProviderUsesProtectedJSONPostAndNoTokenCache(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request=%s %s content-type=%q", r.Method, r.URL.String(), r.Header.Get("Content-Type"))
		}
		var request gnasServiceJWTRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.AppID != "gmzoop-test" || request.AppSecret != "protected-test-secret" {
			t.Fatalf("request=%#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"data":{"token":"short-lived-test-jwt","app_id":"gmzoop-test","expires_at":4102444800}}`)
	}))
	defer server.Close()
	provider := &GNASServiceJWTProvider{Endpoint: server.URL, Client: server.Client(), AppID: "gmzoop-test", Secret: "protected-test-secret", now: time.Now}
	for range 2 {
		if token, err := provider.Token(context.Background()); err != nil || token != "short-lived-test-jwt" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if calls != 2 {
		t.Fatalf("Service JWT calls=%d, want 2", calls)
	}
}

func TestNewGNASAuthorizationResolverRequiresStableSameOriginPaths(t *testing.T) {
	cfg := testUserAuthorizationConfig(t)
	cfg.AuthorizationEndpoint = "https://gnas.example.test/gnas/service/resolveAuthorizationV1"
	cfg.AuthorizationTokenEndpoint = "https://gnas.example.test/gnas/service/getJwtToken"
	cfg.AuthorizationServiceAppID = "gmzoop-test"
	cfg.AuthorizationServiceAppSecret = "protected-test-secret"
	if _, err := NewGNASAuthorizationResolver(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.AuthorizationTokenEndpoint = "https://other.example.test/gnas/service/getJwtToken"
	if _, err := NewGNASAuthorizationResolver(cfg); err == nil {
		t.Fatal("cross-origin token endpoint must fail closed")
	}
	cfg.AuthorizationTokenEndpoint = "https://gnas.example.test/gnas/service/getToken"
	if _, err := NewGNASAuthorizationResolver(cfg); err == nil {
		t.Fatal("legacy token endpoint path must fail closed")
	}
}

func TestHTTPAuthorizationResolverMapsExactGNASFailureContract(t *testing.T) {
	tests := []struct{ status, code int }{{401, 40101}, {403, 40301}, {405, 40501}, {400, 40001}, {503, 50301}}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": test.code, "message": "redacted"})
			}))
			defer server.Close()
			resolver := &HTTPAuthorizationResolver{Endpoint: server.URL, Client: server.Client(), TokenProvider: staticServiceJWTProvider("adapter-test")}
			_, err := resolver.ResolveAuthorization(context.Background(), testAuthorizationQuery())
			var resolveErr *AuthorizationResolveError
			if !errors.As(err, &resolveErr) || resolveErr.HTTPStatus != test.status || resolveErr.BusinessCode != test.code {
				t.Fatalf("err=%v", err)
			}
		})
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

func TestToolsListAndCallResolveAuthorizationAgainWithoutCache(t *testing.T) {
	allowed := testAuthorizationDecision("wecom_record_query")
	revoked := allowed
	revoked.Active = false
	resolver := &sequenceAuthorizationResolver{decisions: []AuthorizationDecision{allowed, revoked}}
	server := newUserAuthorizationHTTPServer(t, RoleReader, resolver, io.Discard)
	defer server.Close()
	tools := listTools(t, server.URL, "reader")
	if !tools["wecom_record_query"] {
		t.Fatalf("tools=%#v", tools)
	}
	response := postRPC(t, server.URL+"/mcp", "reader", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wecom_record_query","arguments":{}}}`)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("post-revoke tools/call status=%d", response.StatusCode)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls=%d, want 2", resolver.calls)
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
			extra["gnas_principal_assertion"] = "signed-principal-test"
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

type staticServiceJWTProvider string

func (p staticServiceJWTProvider) Token(context.Context) (string, error) {
	return string(p), nil
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

func testAuthorizationQuery() AuthorizationQuery {
	return AuthorizationQuery{Tenant: "tenant-test", UserID: "CaseSensitiveUserID", Resource: "gmzoop", PrincipalAssertion: "signed-principal-test"}
}

func testUserAuthorizationConfig(t *testing.T) Config {
	cfg := testServiceConfig(t)
	cfg.RequiredScopes = []string{"wecom.mcp"}
	cfg.UserAuthorizationEnabled = true
	cfg.AuthorizationTenant = "tenant-test"
	cfg.AuthorizationResource = "gmzoop"
	cfg.AuthorizationTimeout = 2 * time.Second
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
			Extra: map[string]any{"role": string(role), "issuer": "https://login.example.test", "wecom_userid": "CaseSensitiveUserID", "gnas_principal_assertion": "signed-principal-test"},
		}, nil
	}
	return httptest.NewServer(service.Handler(verifier))
}

func testToolDefinitions() ([]legacymcp.ToolDefinition, error) {
	return legacymcp.ToolDefinitions()
}
