package team

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestDiscoveryUnreadyHostRecoversAndRevokesWithoutHealthyRebuild(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "discovery-service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewGNASDiscovery(filepath.Join(dir, "policy.json"), dir, "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	makeBinding := func(id string) gnasFleetBinding {
		return gnasFleetBinding{BindingID: id, PublicResource: "https://" + id + ".example", AuthorizationResource: "zoop", Source: "source-" + id, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-" + id, RegistryKey: "key-" + id}}}
	}
	a, b := makeBinding("a"), makeBinding("b")
	remote := []gnasFleetBinding{a, b}
	ready := false
	d.resolveName = func(_ context.Context, runtime instanceconfig.Config) (string, error) {
		if runtime.TenantRoute == "source-b" && !ready {
			return "", errors.New("PRIVATE_UPSTREAM_CANARY")
		}
		return "instance-" + runtime.TenantRoute, nil
	}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	var authorityErr error
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		if authorityErr != nil {
			return nil, authorityErr
		}
		return d.assemble(ctx, signedGNASFleetPayload(t, remote), policy)
	}
	builds := map[string]int{}
	fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(cfg Config) (http.Handler, error) {
		builds[cfg.AuthorizationTenant]++
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(host string, want int) {
		t.Helper()
		for _, path := range []string{"/mcp", "/readyz", "/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
			r := httptest.NewRequest(http.MethodPost, "https://"+host+path, strings.NewReader("PRIVATE_REQUEST_CANARY"))
			r.Header.Set("X-Forwarded-Host", "a.example")
			r.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_CANARY")
			w := httptest.NewRecorder()
			fleet.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "CANARY") || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("unexpected route status or response: host=%s path=%s status=%d", host, path, w.Code)
			}
			if want == 503 && authorityErr == nil && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unready response was cacheable")
			}
		}
	}
	check("a.example", 204)
	check("b.example", 503)
	check("unknown.example", 421)
	if builds["a"] != 1 || builds["b"] != 0 {
		t.Fatal("unready service constructed")
	}
	ready = true
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 204)
	if builds["a"] != 1 || builds["b"] != 1 {
		t.Fatal("healthy service rebuilt or recovered service missing")
	}
	// Identity change discards cached readiness and cannot retain the old service.
	ready = false
	b.Plugins.Zoop = &gnasFleetZoopPlugin{RegistryDocumentID: "registry-b-new", RegistryKey: "key-b"}
	remote = []gnasFleetBinding{a, b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 503)
	if builds["a"] != 1 || builds["b"] != 1 {
		t.Fatal("unready replacement constructed")
	}
	authorityErr = errors.New("authority unavailable")
	if err := fleet.Refresh(t.Context()); err == nil {
		t.Fatal("authority outage ignored")
	}
	check("a.example", 503)
	check("b.example", 503)
	check("unknown.example", 421)
	authorityErr = nil
	remote = []gnasFleetBinding{a}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 421)
	remote = []gnasFleetBinding{b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 421)
	check("b.example", 503)
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("discovery created tenant assets")
	}
}

func TestDiscoveryReadinessProbeDeadlineLeavesPublicationBudget(t *testing.T) {
	d := &GNASDiscovery{stateRoot: t.TempDir(), instances: map[string]discoveredInstance{}}
	d.resolveName = func(ctx context.Context, runtime instanceconfig.Config) (string, error) {
		if runtime.TenantRoute == "source-fast" {
			return "ready", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	bindings := []gnasFleetBinding{
		{BindingID: "slow", Source: "source-slow", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-slow", RegistryKey: "key-slow"}}},
		{BindingID: "fast", Source: "source-fast", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-fast", RegistryKey: "key-fast"}}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	resolved := d.resolveRegistryNames(ctx, bindings, DiscoveryPolicy{}, nil)
	if ctx.Err() != nil || len(resolved) != 1 || resolved["fast"].name != "ready" {
		t.Fatal("slow tenant exhausted shared discovery budget")
	}
}
