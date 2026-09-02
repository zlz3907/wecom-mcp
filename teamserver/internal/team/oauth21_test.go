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
		{name: "missing scope", mutate: func(v map[string]any) { v["scope"] = "zoop.write" }},
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
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()
	tools := listTools(t, server.URL, "opaque-token")
	if len(tools) != 1 || !tools["wecom_schema_status"] {
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
