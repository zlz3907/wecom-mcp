package team

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

func capabilityRuntime(name, source string) instanceconfig.Config {
	return instanceconfig.Config{
		Version: 1, InstanceName: name, TenantRoute: source, RegistryDocumentID: "doc-" + name,
		RegistryKey: "registry-" + name, StatePath: "/tmp/" + name + "-state.json",
		APIWhitelist: map[string][]string{"read": {"get_records"}, "write": {"add_records"}},
	}
}

func newCapabilityService(t *testing.T, serverURL string) *Service {
	t.Helper()
	runtime := capabilityRuntime("instance-a", "source-a")
	cfg := Config{
		Runtime: &runtime, OAuth21ServiceJWT: true, GNASBindingDigest: strings.Repeat("a", 64),
		AuthenticationMode: AuthenticationModeOAuth21, UserAuthorizationEnabled: true,
		AuditHMACKey:       []byte(strings.Repeat("k", 32)),
		MaxConcurrentTools: 4,
		PublicURL:          "https://mcp.example.test/a", MCPURL: "https://mcp.example.test/a/mcp",
		AuthorizationTenant: "binding-a", AuthorizationResource: "resource-a",
		RequiredScopes:               []string{"records:read", "records:write"},
		CapabilityCatalogPushEnabled: true, CapabilityCatalogEndpoint: serverURL + "/gnas/service/reportMCPCapabilityCatalogV1",
		CapabilityCatalogBindingID: "binding-a", CapabilityCatalogSource: "source-a",
		CapabilityCatalogPublicResource:        "https://mcp.example.test/a",
		CapabilityCatalogAuthorizationResource: "resource-a",
		AuthorizationTokenEndpoint:             serverURL + "/gnas/service/getJwtToken",
		AuthorizationServiceAppID:              "mcp-service-a", AuthorizationServiceAppSecret: "protected-service-secret",
		AuthorizationTimeout: time.Second,
	}
	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestCapabilityCatalogUsesActualPluginDefinitionsAndOAuthScopes(t *testing.T) {
	runtime := capabilityRuntime("instance-generic", "source-generic")
	cfg := Config{
		Runtime: &runtime, OAuth21ServiceJWT: true, GNASBindingDigest: strings.Repeat("b", 64),
		AuthenticationMode: AuthenticationModeOAuth21, UserAuthorizationEnabled: true,
		AuditHMACKey:       []byte(strings.Repeat("k", 32)),
		MaxConcurrentTools: 4,
		PublicURL:          "https://mcp.example.test/generic", MCPURL: "https://mcp.example.test/generic/mcp",
		AuthorizationTenant: "binding-generic", AuthorizationResource: "resource-generic",
		RequiredScopes: []string{"records:read"}, CapabilityCatalogPushEnabled: true,
		CapabilityCatalogBindingID: "binding-generic", CapabilityCatalogSource: "source-generic",
		CapabilityCatalogPublicResource:        "https://mcp.example.test/generic",
		CapabilityCatalogAuthorizationResource: "resource-generic",
	}
	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Model an instance with only the generic plugin set. This is the same
	// source used by NewService; the provider must consume definitions, not the
	// legacy package-level tools slice.
	service.definitions, err = legacymcp.ToolDefinitionsForPlugins([]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := service.CapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) == 0 || catalog.Tools[0].Name == "wecom_record_read" {
		t.Fatalf("catalog used the wrong static/plugin tool set: %#v", catalog.Tools)
	}
	if len(catalog.Scopes) != 1 || catalog.Scopes[0] != "records:read" {
		t.Fatalf("catalog scopes did not come from OAuth contract: %#v", catalog.Scopes)
	}
	if strings.Contains(strings.Join(catalog.Scopes, ","), "mcp.group") {
		t.Fatal("APIWhitelist was incorrectly converted into OAuth scopes")
	}

	zoop, err := legacymcp.ToolDefinitionsForPlugins([]string{"zoop"}, true)
	if err != nil {
		t.Fatal(err)
	}
	generic, err := legacymcp.ToolDefinitionsForPlugins([]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(zoop) <= len(generic) {
		t.Fatalf("plugin-specific directory did not differ: zoop=%d generic=%d", len(zoop), len(generic))
	}
}

func TestCapabilityCatalogVersionChangesWhenToolContractChanges(t *testing.T) {
	svc := newCapabilityService(t, "https://gnas.example.test")
	first, err := svc.CapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	svc.definitions = append([]legacymcp.ToolDefinition(nil), svc.definitions...)
	for left, right := 0, len(svc.definitions)-1; left < right; left, right = left+1, right-1 {
		svc.definitions[left], svc.definitions[right] = svc.definitions[right], svc.definitions[left]
	}
	svc.config.RequiredScopes[0], svc.config.RequiredScopes[1] = svc.config.RequiredScopes[1], svc.config.RequiredScopes[0]
	reordered, err := svc.CapabilityCatalog()
	if err != nil || reordered.CatalogVersion != first.CatalogVersion || reordered.Digest != first.Digest {
		t.Fatal("catalog version changed when only the order changed")
	}
	svc.definitions[0].Description += " updated"
	second, err := svc.CapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogVersion == second.CatalogVersion || first.Digest == second.Digest || !strings.HasSuffix(second.CatalogVersion, "-toolpolicy-v1") || len(second.CatalogVersion) > 128 {
		t.Fatal("changed tool definitions did not yield a distinct compatible catalog version")
	}
}

func TestCapabilityCatalogDigestMatchesGNAS858d61aContract(t *testing.T) {
	catalog := capabilityCatalog{
		SchemaVersion: capabilityCatalogSchemaVersion, MCPInstanceID: "instance-a", BindingID: "tenant-a",
		Source: "source-a", PublicResource: "https://mcp-a.example/mcp", AuthorizationResource: "resource-a",
		CatalogVersion: "runtime-v1", Tools: []capabilityCatalogTool{{Name: "record.query", Description: "query records"}, {Name: "record.read", Description: "read records"}}, Scopes: []string{"mcp.group.records"},
	}
	if got, want := capabilityCatalogDigest(catalog), "sha256:731700a177fc63dbd90328c227a20d1c29ac28c1e9532bd480e8e9a3eccaf119"; got != want {
		t.Fatalf("digest=%q want %q", got, want)
	}
	catalog.Scopes = nil
	if got, want := capabilityCatalogDigest(catalog), "sha256:b493f6a7755fd26d9492b304482e28d7a0b76f66e060a764f8412c09141a103a"; got != want {
		t.Fatalf("scope-free digest=%q want %q", got, want)
	}
}

func TestCapabilityCatalogDoesNotRequireOAuthScopes(t *testing.T) {
	svc := newCapabilityService(t, "https://gnas.example.test")
	svc.config.RequiredScopes = nil
	catalog, err := svc.CapabilityCatalog()
	if err != nil || len(catalog.Tools) == 0 || len(catalog.Scopes) != 0 {
		t.Fatalf("scope-free tool catalog unavailable: %+v %v", catalog, err)
	}
}

func TestCapabilityCatalogReportUsesExistingServiceJWTAndBindsIdentity(t *testing.T) {
	var received capabilityCatalogReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "service-token", "app_id": "mcp-service-a", "expires_at": time.Now().Add(time.Minute).Unix()}})
		case "/gnas/service/reportMCPCapabilityCatalogV1":
			if r.Header.Get("Authorization") != "Bearer service-token" || r.Header.Get("X-Auth-Type") != "service_jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "policy_version": received.CatalogVersion, "catalog_digest": received.CatalogDigest, "schema_version": 1, "mcp_instance_id": "binding-a", "app_id": "mcp-service-a", "binding_digest": "sha256:" + strings.Repeat("c", 64), "reported_at": time.Now().UTC().Format(time.RFC3339Nano), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := newCapabilityService(t, server.URL)
	if err := service.PushCapabilityCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.SchemaVersion != capabilityCatalogSchemaVersion || received.BindingID != "binding-a" || received.Source != "source-a" || received.PublicResource != "https://mcp.example.test/a" || received.AuthorizationResource != "resource-a" || received.RequestID == "" || received.Purpose != capabilityCatalogPurpose {
		t.Fatalf("report identity contract mismatch: %#v", received)
	}
	if received.CatalogDigest == "" || received.CatalogDigest != capabilityCatalogDigest(capabilityCatalog{SchemaVersion: received.SchemaVersion, MCPInstanceID: received.MCPInstanceID, BindingID: received.BindingID, Source: received.Source, PublicResource: received.PublicResource, AuthorizationResource: received.AuthorizationResource, CatalogVersion: received.CatalogVersion, Tools: received.Tools, Scopes: received.Scopes}) {
		t.Fatal("report digest does not match canonical GNAS digest")
	}
}

func TestCapabilityCatalogFailsClosedOnCrossBindingScopeAndReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/replay" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "unexpected"})
	}))
	defer server.Close()
	service := newCapabilityService(t, server.URL)
	service.config.CapabilityCatalogBindingID = "binding-other"
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("cross-binding catalog metadata was accepted")
	}
	service.config.CapabilityCatalogBindingID = "binding-a"
	service.config.RequiredScopes = []string{"records:read", "records:read"}
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("duplicate scope was accepted")
	}
	service.config.RequiredScopes = []string{"*"}
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("wildcard scope was accepted")
	}
	service.config.RequiredScopes = nil
	if _, err := service.CapabilityCatalog(); err != nil {
		t.Fatalf("scope-free catalog was rejected: %v", err)
	}
	service.config.RequiredScopes = []string{"records:read"}
	service.config.CapabilityCatalogSource = "source-other"
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("cross-tenant Source was accepted")
	}
	service.config.CapabilityCatalogSource = "source-a"
	service.config.CapabilityCatalogAuthorizationResource = "resource-other"
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("cross-resource authorization metadata was accepted")
	}
	service.config.CapabilityCatalogAuthorizationResource = "resource-a"
	service.config.RequiredScopes = []string{"records:read"}
	service.config.CapabilityCatalogEndpoint = server.URL + "/replay"
	catalog, err := service.CapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	report := capabilityCatalogReport{SchemaVersion: catalog.SchemaVersion, BindingID: catalog.BindingID, Source: catalog.Source, PublicResource: catalog.PublicResource, AuthorizationResource: catalog.AuthorizationResource, CatalogVersion: catalog.CatalogVersion, CatalogDigest: catalog.Digest, Tools: catalog.Tools, Scopes: catalog.Scopes, RequestID: "replayed-request", Purpose: capabilityCatalogPurpose}
	if err := postCapabilityCatalog(context.Background(), server.URL+"/replay", "service-token", report, catalog, server.Client()); err == nil {
		t.Fatal("replayed report was accepted")
	}
}

func TestCapabilityCatalogRejectsRedirectAndTimeout(t *testing.T) {
	catalog := capabilityCatalog{SchemaVersion: capabilityCatalogSchemaVersion, BindingID: "binding-a", Source: "source-a", PublicResource: "https://mcp.example.test/a", AuthorizationResource: "resource-a", CatalogVersion: "version-a", Tools: []capabilityCatalogTool{{Name: "record.read"}}, Scopes: []string{"records:read"}}
	catalog.Digest = capabilityCatalogDigest(catalog)
	report := capabilityCatalogReport{SchemaVersion: catalog.SchemaVersion, BindingID: catalog.BindingID, Source: catalog.Source, PublicResource: catalog.PublicResource, AuthorizationResource: catalog.AuthorizationResource, CatalogVersion: catalog.CatalogVersion, CatalogDigest: catalog.Digest, Tools: catalog.Tools, Scopes: catalog.Scopes, RequestID: "fresh-request", Purpose: capabilityCatalogPurpose}
	redirect := httptest.NewTLSServer(http.RedirectHandler("https://example.invalid/redirect", http.StatusFound))
	defer redirect.Close()
	if err := postCapabilityCatalog(context.Background(), redirect.URL, "service-token", report, catalog, redirect.Client()); err == nil {
		t.Fatal("redirect was followed or accepted")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer slow.Close()
	client := &http.Client{Timeout: 20 * time.Millisecond}
	if err := postCapabilityCatalog(context.Background(), slow.URL, "service-token", report, catalog, client); err == nil {
		t.Fatal("timeout was accepted")
	}
}

func TestCapabilityCatalogReporterRetriesOfflineRecoversAndRefreshes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Millisecond)
	defer cancel()
	var attempts, successes int
	runCapabilityCatalogReporter(ctx, func(context.Context) error {
		attempts++
		if attempts <= 2 {
			return context.DeadlineExceeded
		}
		successes++
		return nil
	}, nil, 12*time.Millisecond, 4*time.Millisecond, 16*time.Millisecond)
	if attempts < 5 || successes < 2 {
		t.Fatalf("reporter did not recover and refresh: attempts=%d successes=%d", attempts, successes)
	}
}

func TestCapabilityCatalogStartupFailureDoesNotBlockMCPHandler(t *testing.T) {
	service := newCapabilityService(t, "http://127.0.0.1:1")
	started := time.Now()
	handler := service.Handler(nil)
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("capability reporting blocked handler startup: %s", elapsed)
	}
	managed, ok := handler.(*managedServiceHandler)
	if !ok {
		t.Fatal("feature-enabled service handler is not lifecycle-managed")
	}
	defer managed.Close()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://mcp.example.test/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("background report failure affected MCP health: status=%d", response.Code)
	}
}

func TestCapabilityRefreshPollIsBoundedToSelectedBinding(t *testing.T) {
	requested := false
	refreshID := strings.Repeat("a", 32)
	polls, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "service-token", "app_id": "mcp-service-a", "expires_at": time.Now().Add(time.Minute).Unix()}})
		case "/gnas/service/pollMCPCapabilityRefreshV1":
			polls++
			var body map[string]string
			if r.Header.Get("Authorization") != "Bearer service-token" || r.Header.Get("X-Auth-Type") != "service_jwt" || json.NewDecoder(r.Body).Decode(&body) != nil || body["binding_id"] != "binding-a" || body["binding_digest"] != strings.Repeat("a", 64) || body["source"] != "source-a" || body["public_resource"] != "https://mcp.example.test/a" || body["authorization_resource"] != "resource-a" {
				t.Error("refresh poll was not bound to the configured resource")
			}
			value := ""
			if requested {
				value = refreshID
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"requested": requested, "refresh_request_id": value}})
		case "/gnas/service/reportMCPCapabilityCatalogV1":
			writes++
			var report capabilityCatalogReport
			if json.NewDecoder(r.Body).Decode(&report) != nil || report.RefreshRequestID != refreshID || report.BindingDigest != strings.Repeat("a", 64) {
				t.Error("report did not bind the pending refresh")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "policy_version": report.CatalogVersion, "catalog_digest": report.CatalogDigest, "schema_version": 1, "mcp_instance_id": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	svc := newCapabilityService(t, server.URL)
	svc.config.CapabilityCatalogPollEndpoint = server.URL + "/gnas/service/pollMCPCapabilityRefreshV1"
	id, err := svc.pollCapabilityRefresh(context.Background())
	if err != nil || id != "" || polls != 1 || writes != 0 {
		t.Fatalf("unselected binding wrote a catalog: id=%q err=%v", id, err)
	}
	requested = true
	id, err = svc.pollCapabilityRefresh(context.Background())
	if err != nil || id != refreshID {
		t.Fatalf("selected binding refresh missing: id=%q err=%v", id, err)
	}
	if err := svc.pushCapabilityCatalog(context.Background(), id); err != nil || writes != 1 {
		t.Fatalf("selected binding report failed: writes=%d err=%v", writes, err)
	}
	refreshID = "invalid"
	if _, err := svc.pollCapabilityRefresh(context.Background()); err == nil {
		t.Fatal("malformed refresh ID was accepted")
	}
}

func TestCapabilityRefreshRejectsDifferentServiceOrigin(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	svc := newCapabilityService(t, server.URL)
	svc.config.CapabilityCatalogPollEndpoint = "https://other.example/gnas/service/pollMCPCapabilityRefreshV1"
	if _, err := svc.pollCapabilityRefresh(context.Background()); err == nil {
		t.Fatal("poll accepted an unrelated service origin")
	}
	svc.config.CapabilityCatalogEndpoint = "https://other.example/gnas/service/reportMCPCapabilityCatalogV1"
	if err := svc.pushCapabilityCatalog(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("report accepted an unrelated service origin")
	}
	if requests.Load() != 0 {
		t.Fatal("origin mismatch obtained a service token")
	}
}

func TestCapabilityBackgroundReportsOnlyAfterSelection(t *testing.T) {
	var requested atomic.Bool
	var polls, writes atomic.Int32
	id := strings.Repeat("b", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "service-token", "app_id": "mcp-service-a", "expires_at": time.Now().Add(time.Minute).Unix()}})
		case "/gnas/service/pollMCPCapabilityRefreshV1":
			polls.Add(1)
			requestID := ""
			if requested.Load() {
				requestID = id
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"requested": requested.Load(), "refresh_request_id": requestID}})
		case "/gnas/service/reportMCPCapabilityCatalogV1":
			var report capabilityCatalogReport
			if json.NewDecoder(r.Body).Decode(&report) != nil || report.RefreshRequestID != id {
				t.Error("background report was not bound to the selection")
			}
			writes.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "policy_version": report.CatalogVersion, "catalog_digest": report.CatalogDigest, "schema_version": 1, "mcp_instance_id": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	svc := newCapabilityService(t, server.URL)
	svc.config.CapabilityCatalogPollEndpoint = server.URL + "/gnas/service/pollMCPCapabilityRefreshV1"
	handler, ok := svc.Handler(nil).(*managedServiceHandler)
	if !ok {
		t.Fatal("catalog worker did not start")
	}
	defer handler.Close()
	deadline := time.Now().Add(2 * time.Second)
	for polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if polls.Load() == 0 || writes.Load() != 0 {
		t.Fatal("unselected binding did not stay read-only")
	}
	requested.Store(true)
	deadline = time.Now().Add(capabilityCatalogPollInterval + 3*time.Second)
	for writes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if writes.Load() != 1 {
		t.Fatalf("selected binding was not reported: %d", writes.Load())
	}
}
