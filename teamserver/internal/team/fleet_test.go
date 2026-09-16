package team

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFleetManifestIsolatesHostsSourcesAndState(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	directory := t.TempDir()
	bindings := make([]FleetBinding, 0, 2)
	for _, suffix := range []string{"a", "b"} {
		instancePath := filepath.Join(directory, "instance-"+suffix+".json")
		instance := map[string]any{
			"version": 1, "instance_name": "instance-" + suffix, "tenant_route": "source-" + suffix,
			"registry_document_id": "registry-" + suffix, "registry_key": "registry-key-" + suffix,
			"schema_mirror_path": filepath.Join(directory, "schema-"+suffix+".json"),
			"state_path":         filepath.Join(directory, "state-"+suffix+".json"),
			"api_whitelist":      map[string]any{"read": []string{"get_records"}},
		}
		writeJSONFile(t, instancePath, instance)
		bindings = append(bindings, FleetBinding{
			BindingID: "binding-" + suffix, Hosts: []string{suffix + ".example.com"},
			PublicURL: "https://" + suffix + ".example.com/gmzoop", AuthorizationTenant: "tenant-" + suffix,
			AuthorizationResource: "gmzoop", Source: "source-" + suffix,
			InstanceConfigPath: instancePath, Plugins: []string{"zoop"},
		})
	}
	manifestPath := filepath.Join(directory, "fleet.json")
	writeJSONFile(t, manifestPath, FleetManifest{Version: 1, Bindings: bindings})
	loaded, err := LoadFleetManifest(manifestPath, "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Config.InstanceConfigPath == loaded[1].Config.InstanceConfigPath || loaded[0].Config.PublicURL == loaded[1].Config.PublicURL {
		t.Fatalf("fleet bindings were not isolated: %#v", loaded)
	}
	if len(loaded[0].Config.Plugins) != 1 || loaded[0].Config.Plugins[0] != "zoop" {
		t.Fatalf("plugin binding was not preserved: %#v", loaded[0].Config.Plugins)
	}
	if loaded[0].Config.OIDCAudience == loaded[1].Config.OIDCAudience || loaded[0].Config.OIDCAudience != "https://a.example.com/gmzoop/mcp" {
		t.Fatalf("OAuth resources were not isolated: %q %q", loaded[0].Config.OIDCAudience, loaded[1].Config.OIDCAudience)
	}
}

func TestLoadFleetManifestRejectsSourceMismatch(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	directory := t.TempDir()
	instancePath := filepath.Join(directory, "instance.json")
	writeJSONFile(t, instancePath, map[string]any{
		"version": 1, "instance_name": "instance-a", "tenant_route": "real-source",
		"registry_document_id": "registry-a", "registry_key": "registry-key-a",
		"schema_mirror_path": filepath.Join(directory, "schema.json"), "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string]any{"read": []string{"get_records"}},
	})
	manifestPath := filepath.Join(directory, "fleet.json")
	writeJSONFile(t, manifestPath, FleetManifest{Version: 1, Bindings: []FleetBinding{{
		BindingID: "binding-a", Hosts: []string{"a.example.com"}, PublicURL: "https://a.example.com/gmzoop",
		AuthorizationTenant: "tenant-a", AuthorizationResource: "gmzoop",
		Source: "wrong-source", InstanceConfigPath: instancePath, Plugins: []string{"zoop"},
	}}})
	if _, err := LoadFleetManifest(manifestPath, "127.0.0.1:17801"); err == nil {
		t.Fatal("fleet source mismatch was accepted")
	}
}

func TestLoadFleetManifestRejectsSharedConnectorCredential(t *testing.T) {
	t.Setenv("TEAM_MCP_AUTH_MODE", "connector_api_key")
	t.Setenv("TEAM_MCP_CONNECTOR_API_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("TEAM_MCP_CONNECTOR_ROLE", "reader")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", "abcdef0123456789abcdef0123456789")
	directory := t.TempDir()
	instancePath := filepath.Join(directory, "instance.json")
	writeJSONFile(t, instancePath, map[string]any{
		"version": 1, "instance_name": "instance-a", "tenant_route": "source-a",
		"registry_document_id": "registry-a", "registry_key": "registry-key-a",
		"schema_mirror_path": filepath.Join(directory, "schema.json"), "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string]any{"read": []string{"get_records"}},
	})
	manifestPath := filepath.Join(directory, "fleet.json")
	writeJSONFile(t, manifestPath, FleetManifest{Version: 1, Bindings: []FleetBinding{{
		BindingID: "binding-a", Hosts: []string{"a.example.com"}, PublicURL: "https://a.example.com/gmzoop",
		AuthorizationTenant: "tenant-a", AuthorizationResource: "gmzoop", Source: "source-a",
		InstanceConfigPath: instancePath, Plugins: []string{"zoop"},
	}}})
	if _, err := LoadFleetManifest(manifestPath, "127.0.0.1:17801"); err == nil {
		t.Fatal("fleet accepted a shared connector credential without tenant-bound OAuth")
	}
}

func setupFleetOAuthEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("TEAM_MCP_AUTH_MODE", "oauth21")
	t.Setenv("TEAM_MCP_OIDC_ISSUER", "https://issuer.example.com/gnas/oauth")
	t.Setenv("TEAM_MCP_OAUTH21_INTROSPECTION_URL", "https://issuer.example.com/gnas/oauth/introspect")
	t.Setenv("TEAM_MCP_OAUTH21_CLIENT_ID", "wecom-mcp-fleet")
	t.Setenv("TEAM_MCP_OAUTH21_CLIENT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", "abcdef0123456789abcdef0123456789")
}

func TestHostRouterUsesOnlyCanonicalRequestHost(t *testing.T) {
	bindings := []LoadedFleetBinding{{Binding: FleetBinding{BindingID: "a", Hosts: []string{"a.example.com"}}}}
	handlers := map[string]http.Handler{"a": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	router, err := NewHostRouter(bindings, handlers)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://a.example.com/healthz", nil)
	request.Host = "a.example.com:443"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("known host status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "https://unknown.example.com/healthz", nil)
	request.Header.Set("X-Forwarded-Host", "a.example.com")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unknown host status=%d", response.Code)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
