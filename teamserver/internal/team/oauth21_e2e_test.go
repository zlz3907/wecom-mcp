package team

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuth21EndToEndMCPClientAndImmediateRevocation(t *testing.T) {
	const (
		accessToken  = "opaque-access-token"
		clientID     = "gmzoop-resource"
		clientSecret = "0123456789abcdef0123456789abcdef"
		issuer       = "https://jyiai.com/gnas/oauth"
		resource     = "https://mcp.jyiai.com/gmzoop/mcp"
	)

	var active atomic.Bool
	active.Store(true)
	var introspectionCalls atomic.Int32
	authorizationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		introspectionCalls.Add(1)
		gotClientID, gotSecret, ok := r.BasicAuth()
		if !ok || gotClientID != clientID || gotSecret != clientSecret {
			t.Fatalf("unexpected resource server credentials")
		}
		if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("token") != accessToken || r.Form.Get("resource") != resource {
			t.Fatalf("unexpected introspection request: method=%s form=%v", r.Method, r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": active.Load(), "iss": issuer, "sub": "wecom:gm:member-001",
			"aud": resource, "exp": time.Now().Add(time.Minute).Unix(), "scope": "zoop.read",
			"token_use": "access", "tenant": "gm", "wecom_userid": "member-001",
			"mcp_role": "member", "effective_tools": []string{"wecom_schema_status"},
		})
	}))
	defer authorizationServer.Close()

	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.PublicURL = "https://mcp.jyiai.com/gmzoop"
	cfg.MCPURL = resource
	cfg.MetadataURL = "https://mcp.jyiai.com/.well-known/oauth-protected-resource/gmzoop/mcp"
	cfg.OIDCIssuer = issuer
	cfg.OIDCAudience = resource
	cfg.OAuth21IntrospectionURL = authorizationServer.URL
	cfg.OAuth21ClientID = clientID
	cfg.OAuth21ClientSecret = clientSecret
	cfg.AuthorizationTenant = "gm"
	cfg.AuthorizationResource = "gmzoop"
	cfg.RequiredScopes = []string{"zoop.read"}

	authenticator, err := NewOAuth21IntrospectionAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	resourceServer := httptest.NewServer(service.Handler(authenticator.Verify))
	defer resourceServer.Close()

	httpClient := &http.Client{Transport: bearerRoundTripper{token: accessToken, base: http.DefaultTransport}}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "oauth21-contract-test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             resourceServer.URL + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "wecom_schema_status" {
		t.Fatalf("tools=%#v", tools.Tools)
	}
	beforeRevocation := introspectionCalls.Load()
	if beforeRevocation < 2 {
		t.Fatalf("introspection calls=%d, want initialize and tools/list to be independently checked", beforeRevocation)
	}

	active.Store(false)
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("tools/list succeeded after authorization was revoked")
	}
	if got := introspectionCalls.Load(); got != beforeRevocation+1 {
		t.Fatalf("introspection calls after revocation=%d, want %d", got, beforeRevocation+1)
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (t bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
