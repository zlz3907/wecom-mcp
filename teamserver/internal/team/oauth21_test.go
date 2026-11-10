package team

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestOAuth21IntrospectionAuthenticatorUsesConfidentialNoCacheLookup(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		clientID, secret, ok := r.BasicAuth()
		if !ok || clientID != "gmzoop-resource" || secret != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("unexpected resource server credentials")
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil || r.Form.Get("token") != "opaque-access-token" || r.Form.Get("resource") != "https://mcp.jyiai.com/gmzoop/mcp" {
			t.Fatalf("unexpected introspection request: method=%s form=%v", r.Method, r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "iss": "https://jyiai.com/gnas/oauth", "sub": "wecom:gm:member-001",
			"aud": "https://mcp.jyiai.com/gmzoop/mcp", "exp": time.Now().Add(time.Minute).Unix(),
			"scope": "zoop.read", "token_use": "access", "tenant": "gm", "wecom_userid": "member-001",
			"mcp_role": "member", "effective_tools": []string{"wecom_schema_status"},
		})
	}))
	defer server.Close()
	cfg := testOAuth21Config(server.URL)
	authenticator, err := NewOAuth21IntrospectionAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		info, err := authenticator.Verify(context.Background(), "opaque-access-token", nil)
		if err != nil || info.UserID != "wecom:gm:member-001" || info.Extra["role"] != string(RolePolicy) {
			t.Fatalf("info=%#v err=%v", info, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("introspection calls=%d, want no-cache call per request", calls.Load())
	}
}

func TestOAuth21IntrospectionAuthenticatorFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "inactive", mutate: func(v map[string]any) { v["active"] = false }},
		{name: "wrong tenant", mutate: func(v map[string]any) { v["tenant"] = "other" }},
		{name: "wrong audience", mutate: func(v map[string]any) { v["aud"] = "https://other.example/mcp" }},
		{name: "expired", mutate: func(v map[string]any) { v["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{name: "unknown shaped tool", mutate: func(v map[string]any) { v["effective_tools"] = []string{"bad/tool"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"active": true, "iss": "https://jyiai.com/gnas/oauth", "sub": "wecom:gm:member-001",
				"aud": "https://mcp.jyiai.com/gmzoop/mcp", "exp": time.Now().Add(time.Minute).Unix(),
				"scope": "zoop.read", "token_use": "access", "tenant": "gm", "wecom_userid": "member-001",
				"mcp_role": "member", "effective_tools": []string{"wecom_schema_status"},
			}
			tc.mutate(payload)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer server.Close()
			authenticator, err := NewOAuth21IntrospectionAuthenticator(testOAuth21Config(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := authenticator.Verify(context.Background(), "opaque-access-token", nil); err == nil || !strings.Contains(err.Error(), sdkauth.ErrInvalidToken.Error()) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOAuth21AcceptsOldAndPolicyAwareServer(t *testing.T) {
	payload := map[string]any{
		"active": true, "iss": "https://jyiai.com/gnas/oauth", "sub": "wecom:gm:member-001",
		"aud": "https://mcp.jyiai.com/gmzoop/mcp", "exp": time.Now().Add(time.Minute).Unix(),
		"scope": "zoop.read", "token_use": "access", "tenant": "gm", "wecom_userid": "member-001",
		"mcp_role": "member", "effective_tools": []string{"wecom_schema_status"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	cfg := testOAuth21Config(server.URL)
	cfg.CapabilityCatalogPushEnabled = true
	authenticator, err := NewOAuth21IntrospectionAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err != nil {
		t.Fatal("bootstrap rejected legacy server")
	}
	payload["tool_policy_contract_v1"] = true
	payload["tool_policy_managed"] = false
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err != nil {
		t.Fatal("policy-aware server was rejected")
	}
	delete(payload, "tool_policy_managed")
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err == nil {
		t.Fatal("policy contract with missing managed flag was accepted")
	}
	payload["tool_policy_managed"] = nil
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err == nil {
		t.Fatal("policy contract with null managed flag was accepted")
	}
	payload["tool_policy_managed"] = true
	for _, value := range []any{nil, "", " revision", "bad/revision"} {
		payload["tool_policy_revision"] = value
		if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err == nil {
			t.Fatalf("managed policy with invalid revision %#v was accepted", value)
		}
	}
	delete(payload, "tool_policy_revision")
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err == nil {
		t.Fatal("managed policy with missing revision was accepted")
	}
	payload["tool_policy_revision"] = "revision-test"
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err != nil {
		t.Fatalf("managed policy with valid revision rejected: %v", err)
	}
	payload["tool_policy_contract_v1"] = false
	if _, err := authenticator.Verify(context.Background(), "opaque-token", nil); err == nil {
		t.Fatal("managed policy without contract was accepted")
	}
}

func TestOAuth21ManagedPoliciesKeepOtherScopes(t *testing.T) {
	cfg := testOAuth21Config("https://example.invalid/introspect")
	cfg.RequiredScopes = []string{"zoop.write", "other.permission"}
	response := oauth21IntrospectionResponse{Active: true, Issuer: cfg.OIDCIssuer, Subject: "employee:member", Audience: json.RawMessage(`"https://mcp.jyiai.com/gmzoop/mcp"`), ExpiresAt: time.Now().Add(time.Minute).Unix(), Scope: "zoop.read", TokenUse: "access", Tenant: cfg.AuthorizationTenant, WeComUserID: "member", MCPRole: "member", EffectiveTools: []string{"wecom_record_apply"}, PolicyContractV1: true, ManagedToolPolicy: boolPointer(false)}
	if validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("managed contract bypassed a non-Zoop scope")
	}
	cfg.RequiredScopes = []string{"zoop.write"}
	if !validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("valid policy contract rejected")
	}
	response.PolicyContractV1 = false
	if !validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("legacy Zoop scope label must not restrict a verified employee")
	}
	response.Scope = ""
	if !validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("legacy empty Zoop scope must not restrict a verified employee")
	}
	cfg.RequiredScopes = []string{"zoop.read", "other.permission"}
	response.Scope = "zoop.write"
	if validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("unrelated required scope was bypassed")
	}
	response.Scope = "other.permission"
	if !validOAuth21IntrospectionResponse(response, cfg, time.Now()) {
		t.Fatal("unrelated required scope was not recognized")
	}
}

func TestOAuth21LegacyZoopScopeLabelsDoNotLimitToolList(t *testing.T) {
	for _, scope := range []string{"zoop.read", "zoop.write", "", "zoop.read zoop.write"} {
		t.Run("scope="+scope, func(t *testing.T) {
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"active": true, "iss": "https://jyiai.com/gnas/oauth", "sub": "wecom:gm:member-001",
					"aud": "https://mcp.jyiai.com/gmzoop/mcp", "exp": time.Now().Add(time.Minute).Unix(),
					"scope": scope, "token_use": "access", "tenant": "gm", "wecom_userid": "member-001",
					"mcp_role": "member", "effective_tools": []string{"*"},
				})
			}))
			defer authority.Close()
			cfg := testServiceConfig(t)
			cfg.AuthenticationMode, cfg.UserAuthorizationEnabled = AuthenticationModeOAuth21, true
			cfg.OIDCIssuer, cfg.OIDCAudience = "https://jyiai.com/gnas/oauth", "https://mcp.jyiai.com/gmzoop/mcp"
			cfg.OAuth21IntrospectionURL = authority.URL
			cfg.OAuth21ClientID, cfg.OAuth21ClientSecret = "gmzoop-resource", "0123456789abcdef0123456789abcdef"
			cfg.AuthorizationTenant, cfg.AuthorizationResource = "gm", "gmzoop"
			cfg.RequiredScopes = []string{"zoop.read", "zoop.write"}
			cfg.OIDCHTTPTimeout = time.Second
			authenticator, err := NewOAuth21IntrospectionAuthenticator(cfg)
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			installTestPolicyFloor(t, service, "")
			resource := httptest.NewServer(service.Handler(authenticator.Verify))
			defer resource.Close()
			tools := listTools(t, resource.URL, "opaque-token")
			if !tools["wecom_record_query"] || !tools["wecom_record_apply"] {
				t.Fatalf("Zoop tool availability changed for scope %q: %v", scope, tools)
			}
		})
	}
}

func TestOAuth21ServiceFiltersToolsFromCurrentIntrospectionPolicy(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.RequiredScopes = []string{"zoop.read"}
	cfg.AuthorizationTenant = "gm"
	cfg.AuthorizationResource = "gmzoop"
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Minute), Scopes: []string{"zoop.read"}, UserID: "wecom:gm:member-001",
			Extra: map[string]any{"role": string(RolePolicy), "wecom_userid": "member-001", "mcp_role": "member", "effective_tools": []string{"wecom_schema_status"}},
		}, nil
	}
	installTestPolicyFloor(t, service, "")
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()
	tools := listTools(t, server.URL, "opaque-token")
	if len(tools) != 2 || !tools["wecom_schema_status"] || !tools["wecom_employee_list"] {
		t.Fatalf("tools=%v", tools)
	}
}

func testOAuth21Config(introspectionURL string) Config {
	return Config{
		AuthenticationMode: AuthenticationModeOAuth21, OAuth21IntrospectionURL: introspectionURL,
		OAuth21ClientID: "gmzoop-resource", OAuth21ClientSecret: "0123456789abcdef0123456789abcdef",
		OIDCIssuer: "https://jyiai.com/gnas/oauth", OIDCAudience: "https://mcp.jyiai.com/gmzoop/mcp",
		AuthorizationTenant: "gm", RequiredScopes: []string{"zoop.read"}, OIDCHTTPTimeout: time.Second,
	}
}

func TestOAuth21ManagedToolPolicyRejectsAmbiguousClaims(t *testing.T) {
	cfg := testOAuth21Config("https://example.invalid/introspect")
	cfg.RequiredScopes = []string{"zoop.read"}
	base := oauth21IntrospectionResponse{Active: true, Issuer: cfg.OIDCIssuer, Subject: "employee:member", Audience: json.RawMessage(`"https://mcp.jyiai.com/gmzoop/mcp"`), ExpiresAt: time.Now().Add(time.Minute).Unix(), TokenUse: "access", Tenant: cfg.AuthorizationTenant, WeComUserID: "member", MCPRole: "member", ManagedToolPolicy: boolPointer(true), PolicyContractV1: true, PolicyRevision: "revision-test"}
	if !validOAuth21IntrospectionResponse(base, cfg, time.Now()) {
		t.Fatal("an explicit managed policy granting zero tools must remain a valid identity")
	}
	base.PolicyRevision = ""
	if validOAuth21IntrospectionResponse(base, cfg, time.Now()) {
		t.Fatal("managed policy without revision must be rejected")
	}
	base.PolicyRevision = "revision-test"
	base.PolicyContractV1 = false
	if validOAuth21IntrospectionResponse(base, cfg, time.Now()) {
		t.Fatal("managed policy without contract must be rejected")
	}
	base.PolicyContractV1 = true
	base.EffectiveTools = []string{"*"}
	if validOAuth21IntrospectionResponse(base, cfg, time.Now()) {
		t.Fatal("managed policy may not grant an unbounded wildcard")
	}
	base.EffectiveTools = []string{"wecom_record_query", "wecom_record_query"}
	if validOAuth21IntrospectionResponse(base, cfg, time.Now()) {
		t.Fatal("duplicate tool claims must be rejected")
	}
}

func boolPointer(value bool) *bool { return &value }
