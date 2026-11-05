package team

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCapabilityCatalogDigestMatchesGNAS858d61aContract(t *testing.T) {
	catalog := capabilityCatalog{
		SchemaVersion: capabilityCatalogSchemaVersion, MCPInstanceID: "instance-a", BindingID: "tenant-a",
		Source: "source-a", PublicResource: "https://mcp-a.example/mcp", AuthorizationResource: "resource-a",
		CatalogVersion: "runtime-v1", Tools: []capabilityCatalogTool{{Name: "record.query", Description: "query records"}, {Name: "record.read", Description: "read records"}}, Scopes: []string{"mcp.group.records"},
	}
	if got, want := capabilityCatalogDigest(catalog), "sha256:731700a177fc63dbd90328c227a20d1c29ac28c1e9532bd480e8e9a3eccaf119"; got != want {
		t.Fatalf("digest=%q want %q", got, want)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "policy_version": received.CatalogVersion, "catalog_digest": received.CatalogDigest, "schema_version": 1, "mcp_instance_id": ""})
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
	if _, err := service.CapabilityCatalog(); err == nil {
		t.Fatal("missing OAuth/plugin scope was accepted")
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
