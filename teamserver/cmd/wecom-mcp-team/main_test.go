package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/teamserver/internal/team"
)

// Exercise the assembled resource server, not just a stub tenant handler.
// The same HTTP listener must serve a new tenant after an in-memory refresh.
func TestRefreshingResourceServerRoutesAndChallenges(t *testing.T) {
	t.Setenv("TEAM_MCP_AUTH_MODE", "oauth21")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", strings.Repeat("a", 32))
	t.Setenv("TEAM_MCP_OAUTH21_INTROSPECTION_URL", "http://127.0.0.1:1/gnas/oauth/introspect")
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "test-app")
	t.Setenv("GNAS_APP_SECRET", "test-app-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir := t.TempDir()
	makeBinding := func(id string) team.LoadedFleetBinding {
		t.Helper()
		runtime := instanceconfig.Config{Version: 1, InstanceName: id, TenantRoute: "source-" + id, RegistryDocumentID: "doc-" + id, RegistryKey: "registry-" + id, SchemaSource: "z-s00", StatePath: filepath.Join(dir, id+"-state.json"), APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
		cfg, err := team.LoadConfigForBinding("", "127.0.0.1:17801", team.BindingOverrides{
			Runtime: &runtime, OAuth21ServiceJWT: true,
			GNASBindingDigest: strings.Repeat("a", 64),
			PublicURL:         "https://" + id + ".example", OIDCIssuer: "https://" + id + ".example/gnas/oauth",
			AuthorizationTenant: id, AuthorizationResource: "zoop", Plugins: []string{"zoop"},
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg.TrustedLoopbackProxy = true
		return team.LoadedFleetBinding{Binding: team.FleetBinding{BindingID: id, Hosts: []string{id + ".example"}}, Config: cfg}
	}
	a, b := makeBinding("a"), makeBinding("b")
	loaded := []team.LoadedFleetBinding{a}
	var loadErr error
	fleet, err := team.NewRefreshingFleet(t.Context(), func(context.Context) ([]team.LoadedFleetBinding, error) {
		return loaded, loadErr
	}, func(cfg team.Config) (http.Handler, error) {
		return bindingHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fleet)
	defer server.Close()
	check := func(host, path string, want int) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host + ".example"
		req.Header.Set("X-Forwarded-Host", "a.example")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s%s: status=%d want=%d readErr=%v", host, path, res.StatusCode, want, err)
		}
		if want == 401 && !strings.Contains(res.Header.Get("WWW-Authenticate"), host+".example") {
			t.Fatal("challenge did not advertise the selected tenant")
		}
		return string(body)
	}
	check("a", "/healthz", 200)
	check("b", "/mcp", 421)
	loaded = []team.LoadedFleetBinding{a, b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		check(id, "/healthz", 200)
		check(id, "/readyz", 200)
		check(id, "/mcp", 401)
		body := check(id, "/.well-known/oauth-protected-resource/mcp", 200)
		if !strings.Contains(body, "https://"+id+".example/mcp") {
			t.Fatal("metadata resource crossed tenant boundary")
		}
	}
	loadErr = errors.New("test control plane unavailable")
	if err := fleet.Refresh(t.Context()); err == nil {
		t.Fatal("resolver failure accepted")
	}
	for _, path := range []string{"/healthz", "/readyz", "/mcp", "/.well-known/oauth-protected-resource/mcp"} {
		check("a", path, 503)
		check("b", path, 503)
		check("unknown", path, 421)
	}
	loadErr, loaded = nil, []team.LoadedFleetBinding{b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a", "/mcp", 421)
	check("b", "/mcp", 401)
}
