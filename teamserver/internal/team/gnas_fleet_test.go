package team

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadGNASFleetUsesRemoteBindingAuthority(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("COMPANY_A_INTROSPECTION_SECRET", strings.Repeat("a", 32))
	directory := t.TempDir()
	instancePath := filepath.Join(directory, "instance.json")
	writeJSONFile(t, instancePath, map[string]any{
		"version": 1, "instance_name": "company-a", "tenant_route": "wecom-company-a",
		"registry_document_id": "doc-a", "registry_key": "company_a_zoop_v1",
		"schema_mirror_path": filepath.Join(directory, "schema.json"), "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string]any{"read": []string{"get_records"}},
	})
	runtimePath := filepath.Join(directory, "runtime.json")
	writeJSONFile(t, runtimePath, GNASFleetRuntimeManifest{Version: 1, Bindings: []GNASFleetRuntimeBinding{{BindingID: "company_a", InstanceConfigPath: instancePath, ClientID: "company-a-resource", ClientSecretEnv: "COMPANY_A_INTROSPECTION_SECRET"}}})
	payload := signedGNASFleetPayload(t, []gnasFleetBinding{{
		BindingID: "company_a", PublicResource: "https://mcp.company-a.example", AuthorizationResource: "existing_policy", Source: "wecom-company-a",
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
	if len(loaded) != 1 || loaded[0].Binding.Source != "wecom-company-a" || loaded[0].Binding.Hosts[0] != "mcp.company-a.example" || loaded[0].Binding.AuthorizationResource != "existing_policy" || loaded[0].Config.AuthorizationResource != "existing_policy" || !loaded[0].Config.TrustedLoopbackProxy || loaded[0].Config.OIDCAudience != "https://mcp.company-a.example/mcp" || loaded[0].Config.OIDCIssuer != "https://mcp.company-a.example/gnas/oauth" {
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
		BindingID: "company_a", PublicResource: "https://mcp.company-a.example", AuthorizationResource: "company_a_zoop", Source: "wecom-company-a",
		Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "remote-doc", RegistryKey: "company_a_zoop_v1"}},
	}})
	if _, err := mergeGNASFleet(payload, map[string]GNASFleetRuntimeBinding{"company_a": {InstanceConfigPath: instancePath}}, "127.0.0.1:17801"); err == nil {
		t.Fatal("registry drift was accepted")
	}
}

func TestGNASFleetPerBindingCredentials(t *testing.T) {
	for _, scenario := range []string{"distinct", "missing_id", "missing_env", "unset_secret", "short_secret", "duplicate_client", "invalid_env", "oidc"} {
		t.Run(scenario, func(t *testing.T) {
			setupFleetOAuthEnvironment(t) // Valid global credentials must never be a fallback.
			dir := t.TempDir()
			manifest := GNASFleetRuntimeManifest{Version: 1}
			var remote []gnasFleetBinding
			for _, name := range []string{"a", "b"} {
				instance := filepath.Join(dir, name+".json")
				writeJSONFile(t, instance, map[string]any{
					"version": 1, "instance_name": name, "tenant_route": "source-" + name,
					"registry_document_id": "doc-" + name, "registry_key": "registry-" + name,
					"state_path":    filepath.Join(dir, name+"-state.json"),
					"api_whitelist": map[string]any{"read": []string{"get_records"}},
				})
				secretEnv := "TEST_FLEET_SECRET_" + strings.ToUpper(name)
				t.Setenv(secretEnv, strings.Repeat(name, 32))
				manifest.Bindings = append(manifest.Bindings, GNASFleetRuntimeBinding{BindingID: name, InstanceConfigPath: instance, ClientID: "resource-" + name, ClientSecretEnv: secretEnv})
				remote = append(remote, gnasFleetBinding{BindingID: name, PublicResource: "https://" + name + ".example", AuthorizationResource: "zoop", Source: "source-" + name, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "doc-" + name, RegistryKey: "registry-" + name}}})
			}
			switch scenario {
			case "missing_id":
				manifest.Bindings[1].ClientID = ""
			case "missing_env":
				manifest.Bindings[1].ClientSecretEnv = ""
			case "unset_secret":
				t.Setenv("TEST_FLEET_SECRET_B", "")
			case "short_secret":
				t.Setenv("TEST_FLEET_SECRET_B", "short")
			case "duplicate_client":
				manifest.Bindings[1].ClientID = manifest.Bindings[0].ClientID
			case "invalid_env":
				manifest.Bindings[1].ClientSecretEnv = "invalid-env"
			case "oidc":
				t.Setenv("TEAM_MCP_AUTH_MODE", "oidc")
			}
			path := filepath.Join(dir, "runtime.json")
			writeJSONFile(t, path, manifest)
			bindings, err := loadGNASFleetRuntimeManifest(path)
			var loaded []LoadedFleetBinding
			if err == nil {
				loaded, err = mergeGNASFleet(signedGNASFleetPayload(t, remote), bindings, "127.0.0.1:17801")
			}
			if scenario != "distinct" {
				if err == nil {
					t.Fatal("invalid per-binding configuration accepted")
				}
				if scenario == "oidc" && !strings.Contains(err.Error(), "requires oauth21") {
					t.Fatalf("wrong rejection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded) != 2 {
				t.Fatalf("loaded count=%d", len(loaded))
			}
			for i, name := range []string{"a", "b"} {
				if loaded[i].Config.OAuth21ClientID != "resource-"+name || loaded[i].Config.OAuth21ClientSecret != strings.Repeat(name, 32) {
					t.Fatalf("binding %s did not use its exact credentials", name)
				}
			}
		})
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
