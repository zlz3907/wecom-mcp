package team

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadGNASFleetUsesRemoteBindingAuthority(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	directory := t.TempDir()
	instancePath := filepath.Join(directory, "instance.json")
	writeJSONFile(t, instancePath, map[string]any{
		"version": 1, "instance_name": "company-a", "tenant_route": "wecom-company-a",
		"registry_document_id": "doc-a", "registry_key": "company_a_zoop_v1",
		"schema_mirror_path": filepath.Join(directory, "schema.json"), "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string]any{"read": []string{"get_records"}},
	})
	runtimePath := filepath.Join(directory, "runtime.json")
	writeJSONFile(t, runtimePath, GNASFleetRuntimeManifest{Version: 1, Bindings: []GNASFleetRuntimeBinding{{BindingID: "company_a", InstanceConfigPath: instancePath}}})
	payload := signedGNASFleetPayload(t, []gnasFleetBinding{{
		BindingID: "company_a", PublicResource: "https://mcp.company-a.example", Source: "wecom-company-a",
		Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "doc-a", RegistryKey: "company_a_zoop_v1"}},
	}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gnas/service/getJwtToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "service-token", "app_id": "codex-macos-admin", "expires_at": time.Now().Add(time.Hour).Unix()}})
		case "/gnas/service/resolveMCPBindingsV1":
			if r.Header.Get("Authorization") != "Bearer service-token" || r.Header.Get("X-Auth-Type") != "service_jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": payload})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("GNAS_BASE_URL", server.URL)
	t.Setenv("GNAS_APP_ID", "codex-macos-admin")
	t.Setenv("GNAS_APP_SECRET", "protected-test-secret")

	loaded, err := LoadGNASFleet(t.Context(), runtimePath, "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Binding.Source != "wecom-company-a" || loaded[0].Binding.Hosts[0] != "mcp.company-a.example" || !loaded[0].Config.TrustedLoopbackProxy || loaded[0].Config.OIDCAudience != "https://mcp.company-a.example/mcp" || loaded[0].Config.OIDCIssuer != "https://mcp.company-a.example/gnas/oauth" {
		t.Fatalf("loaded=%#v", loaded)
	}
}

func TestMergeGNASFleetRejectsRegistryDrift(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	directory := t.TempDir()
	instancePath := filepath.Join(directory, "instance.json")
	writeJSONFile(t, instancePath, map[string]any{
		"version": 1, "instance_name": "company-a", "tenant_route": "wecom-company-a",
		"registry_document_id": "local-doc", "registry_key": "company_a_zoop_v1",
		"schema_mirror_path": filepath.Join(directory, "schema.json"), "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string]any{"read": []string{"get_records"}},
	})
	payload := signedGNASFleetPayload(t, []gnasFleetBinding{{
		BindingID: "company_a", PublicResource: "https://mcp.company-a.example", Source: "wecom-company-a",
		Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "remote-doc", RegistryKey: "company_a_zoop_v1"}},
	}})
	if _, err := mergeGNASFleet(payload, map[string]string{"company_a": instancePath}, "127.0.0.1:17801"); err == nil {
		t.Fatal("registry drift was accepted")
	}
}

func signedGNASFleetPayload(t *testing.T, bindings []gnasFleetBinding) gnasFleetPayload {
	t.Helper()
	canonical, err := json.Marshal(struct {
		Version  int                `json:"version"`
		Bindings []gnasFleetBinding `json:"bindings"`
	}{Version: 1, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return gnasFleetPayload{Version: 1, Digest: hex.EncodeToString(digest[:]), Bindings: bindings}
}
