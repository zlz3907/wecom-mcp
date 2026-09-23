package team

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestGNASDiscoveryDerivesInstancesWithoutTenantFiles(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "discovery-service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewGNASDiscovery(filepath.Join(root, "policy.json"), root, "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	names := 0
	d.resolveName = func(_ context.Context, cfg instanceconfig.Config) (string, error) {
		names++
		if cfg.RegistryDocumentID == "unavailable" {
			return "", errors.New("test registry unavailable")
		}
		return "existing-" + cfg.TenantRoute, nil
	}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	binding := func(id string) gnasFleetBinding {
		return gnasFleetBinding{BindingID: id, PublicResource: "https://" + id + ".example", AuthorizationResource: "zoop", Source: "source-" + id, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-" + id, RegistryKey: "key-" + id}}}
	}
	a, b := binding("a"), binding("b")
	load := func(bindings ...gnasFleetBinding) ([]LoadedFleetBinding, error) {
		if bindings == nil {
			bindings = []gnasFleetBinding{}
		}
		return d.assemble(t.Context(), signedGNASFleetPayload(t, bindings), policy)
	}
	first, err := load(a)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Config.InstanceConfigPath != "" || first[0].Config.Runtime.InstanceName != "existing-source-a" || !first[0].Config.OAuth21ServiceJWT || first[0].Config.OAuth21ClientSecret != "" {
		t.Fatal("database mode retained a local tenant dependency")
	}
	second, err := load(a, b)
	if err != nil || len(second) != 2 || names != 2 {
		t.Fatalf("add: count=%d resolutions=%d err=%v", len(second), names, err)
	}
	if second[0].Config.Runtime.StatePath == second[1].Config.Runtime.StatePath {
		t.Fatal("tenants share state")
	}
	third, err := load(b)
	if err != nil || len(third) != 1 || names != 2 {
		t.Fatal("removal or unchanged cache failed")
	}
	b.Source = "source-new"
	changed, err := load(b)
	if err != nil || changed[0].Config.Runtime.StatePath == third[0].Config.Runtime.StatePath || changed[0].Config.GNASBindingDigest == third[0].Config.GNASBindingDigest {
		t.Fatal("changed identity reused old state")
	}
	bad := binding("c")
	bad.Plugins.Zoop.RegistryDocumentID = "unavailable"
	partial, err := load(b, bad)
	if err != nil || len(partial) != 2 || partial[0].RegistryUnavailable || !partial[1].RegistryUnavailable {
		t.Fatal("incomplete Registry did not isolate its authoritative Host")
	}
	if len(d.instances) != 1 {
		t.Fatal("partial candidate published")
	}
	empty, err := load()
	if err != nil || len(empty) != 0 || len(d.instances) != 0 {
		t.Fatal("authoritative empty snapshot rejected")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("discovery wrote per-tenant files")
	}
	if _, err := load(a, a); err == nil {
		t.Fatal("duplicate binding accepted")
	}
}

func TestDiscoveryPolicyRejectsTenantConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	writeJSONFile(t, path, map[string]any{"version": 1, "api_whitelist": map[string][]string{"read": {"get_records"}}, "bindings": []string{"a"}})
	if _, err := loadDiscoveryPolicy(path); err == nil {
		t.Fatal("per-tenant configuration accepted in uniform policy")
	}
}

// This vector is shared with the GNAS endpoint's contract test. Changes to
// struct fields or their encoding require an explicit protocol migration.
func TestGNASBindingDigestContractVector(t *testing.T) {
	b := gnasFleetBinding{BindingID: "company_a", PublicResource: "https://a.example", AuthorizationResource: "zoop", Source: "source-a", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-a", RegistryKey: "key-a"}}}
	if got := gnasBindingDigest(b); got != "f7ad6cdd8bc55e76b14aff6a6cb2b1e3040b9b50c54f01205f508d534cd1a142" {
		t.Fatalf("binding digest contract changed: %s", got)
	}
}
