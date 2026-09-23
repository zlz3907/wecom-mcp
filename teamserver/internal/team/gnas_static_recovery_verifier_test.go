package team

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type staticRecoveryFixture struct {
	discovery *GNASDiscovery
	runtime   instanceconfig.Config
	localPath string
	locals    map[string]GNASFleetRuntimeBinding
	policy    DiscoveryPolicy
	bindings  []gnasFleetBinding
}

func newStaticRecoveryFixture(t *testing.T) staticRecoveryFixture {
	t.Helper()
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "test-service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := instanceconfig.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "static-state.json"), WecomOperatorUserID: "test-operator", AIExecutionSubjectRecordID: "test-executor", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}, "zoop_records_write": {"list_employees", "add_records", "update_records"}, "app_message": {"list_employees", "send_app_message"}, "schema_migration": {"list_employees", "get_sheet", "get_fields", "get_records", "add_fields"}}}
	localPath := filepath.Join(dir, "static.json")
	writeJSONFile(t, localPath, runtime)
	d, err := NewGNASStaticRecovery(filepath.Join(dir, "policy.json"), dir, filepath.Join(dir, "runtime.json"), "127.0.0.1:17801", "https://a.example")
	if err != nil {
		t.Fatal(err)
	}
	d.resolveName = func(context.Context, instanceconfig.Config) (string, error) {
		t.Error("static recovery attempted Registry discovery")
		return "", errors.New("Registry must be unused")
	}
	return staticRecoveryFixture{discovery: d, runtime: runtime, localPath: localPath, locals: map[string]GNASFleetRuntimeBinding{"a": {BindingID: "a", InstanceConfigPath: localPath}}, policy: DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}, bindings: []gnasFleetBinding{
		{BindingID: "a", PublicResource: "https://a.example", AuthorizationResource: "zoop", Source: "source-a", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-a", RegistryKey: "key-a"}}},
		{BindingID: "b", PublicResource: "https://b.example", AuthorizationResource: "zoop", Source: "source-b", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-b", RegistryKey: "key-b"}}},
	}}
}

func TestStaticRecoveryVerifierPreservesBoundCapabilities(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	loaded, err := f.discovery.assembleWithLocals(t.Context(), signedGNASFleetPayload(t, f.bindings), f.policy, f.locals)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("published %d bindings; want protected static only", len(loaded))
	}
	cfg := loaded[0].Config
	if cfg.Runtime != nil || cfg.BoundRuntime == nil || !cfg.OAuth21ServiceJWT || cfg.GNASBindingDigest != gnasBindingDigest(f.bindings[0]) || cfg.BoundRuntime.Digest() != f.runtime.Digest() || cfg.AuthorizationTenant != "a" || cfg.PublicURL != "https://a.example" {
		t.Fatal("static runtime identity, digest, or authentication changed")
	}
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Minute), UserID: "test-employee", Scopes: cfg.RequiredScopes, Extra: map[string]any{"role": string(RolePolicy), "issuer": cfg.OIDCIssuer, "wecom_userid": "test-employee", "mcp_role": "admin", "effective_tools": []string{"*"}}}, nil
	}
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()
	names := listTools(t, server.URL, "test-verified")
	for _, name := range []string{"wecom_record_query", "wecom_record_apply", "wecom_send_app_message", "wecom_schema_migration_apply", "wecom_instance_initialize", "wecom_instance_initialize_status"} {
		if !names[name] {
			t.Errorf("static capability missing: %s", name)
		}
	}
	drift := f.runtime
	drift.TenantRoute = "source-unapproved"
	writeJSONFile(t, f.localPath, drift)
	response, err := server.Client().Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("runtime config drift served readiness %d", response.StatusCode)
	}
	writeJSONFile(t, f.localPath, f.runtime)
	if !listTools(t, server.URL, "test-verified")["wecom_record_apply"] {
		t.Fatal("restored bound runtime did not recover")
	}
}

func TestStaticRecoveryVerifierRevocationFailureAndRecovery(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	remote := f.bindings
	var outage error
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		if outage != nil {
			return nil, outage
		}
		return f.discovery.assembleWithLocals(ctx, signedGNASFleetPayload(t, remote), f.policy, f.locals)
	}
	fleet, err := NewDiscoveryFleet(t.Context(), f.discovery.listen, load, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(host string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://"+host+"/readyz", nil)
		r.Header.Set("X-Forwarded-Host", "a.example")
		w := httptest.NewRecorder()
		fleet.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("host=%s status=%d want=%d", host, w.Code, want)
		}
	}
	check("a.example", 200)
	check("b.example", 421)
	outage = errors.New("fake authority unavailable")
	if fleet.Refresh(t.Context()) == nil {
		t.Fatal("authority outage accepted")
	}
	check("a.example", 503)
	check("b.example", 421)
	check("unknown.example", 421)
	outage = nil
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 200)
	drift := f.runtime
	drift.RegistryKey = "unapproved-key"
	writeJSONFile(t, f.localPath, drift)
	if fleet.Refresh(t.Context()) == nil {
		t.Fatal("mapped identity drift accepted")
	}
	check("a.example", 503)
	writeJSONFile(t, f.localPath, f.runtime)
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 200)
	moved := f.bindings[0]
	moved.PublicResource = "https://moved.example"
	remote = []gnasFleetBinding{moved, f.bindings[1]}
	if fleet.Refresh(t.Context()) == nil {
		t.Fatal("public URL drift accepted")
	}
	check("a.example", 503)
	check("moved.example", 421)
	remote = []gnasFleetBinding{f.bindings[1]}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 421)
	check("b.example", 421)
	remote = f.bindings
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 200)
	remote = []gnasFleetBinding{}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 421)
	outage = errors.New("outage after empty snapshot")
	if fleet.Refresh(t.Context()) == nil {
		t.Fatal("post-empty outage accepted")
	}
	check("a.example", 421)
}

func TestStaticRecoveryVerifierRequiresSingleMappingAndExactURL(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	for _, locals := range []map[string]GNASFleetRuntimeBinding{nil, {}, {"a": f.locals["a"], "b": {BindingID: "b", InstanceConfigPath: f.localPath}}} {
		if _, err := f.discovery.assembleWithLocals(t.Context(), signedGNASFleetPayload(t, f.bindings), f.policy, locals); err == nil {
			t.Errorf("accepted %d static mappings", len(locals))
		}
	}
	for _, publicURL := range []string{"", "http://a.example", "https://a.example/", "https://a.example?tenant=b", "https://user@a.example", "https://a.example:443", "https://a.example/#fragment", "https://a.example/a/b"} {
		if _, err := NewGNASStaticRecovery(f.discovery.policyPath, f.discovery.stateRoot, f.discovery.runtimeManifestPath, f.discovery.listen, publicURL); err == nil {
			t.Errorf("accepted invalid fixed URL %q", publicURL)
		}
	}
	// Filtering is not permission to accept corrupt or ambiguous authority snapshots.
	duplicate := append(append([]gnasFleetBinding{}, f.bindings...), f.bindings[1])
	if _, err := f.discovery.assembleWithLocals(t.Context(), signedGNASFleetPayload(t, duplicate), f.policy, f.locals); err == nil {
		t.Fatal("duplicate unmapped authority accepted")
	}
	tampered := signedGNASFleetPayload(t, f.bindings)
	tampered.Digest = "invalid"
	if _, err := f.discovery.assembleWithLocals(t.Context(), tampered, f.policy, f.locals); err == nil {
		t.Fatal("invalid authority digest accepted")
	}
}
