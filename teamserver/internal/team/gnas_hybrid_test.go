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

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestHybridFleetPreservesStaticToolsAndRemovesDeletedAuthority(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(dir, "local.json")
	runtime := instanceconfig.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "static-state.json"), WecomOperatorUserID: "test-operator", AIExecutionSubjectRecordID: "test-executor", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}, "zoop_records_write": {"list_employees", "add_records", "update_records"}, "app_message": {"list_employees", "send_app_message"}, "schema_migration": {"list_employees", "get_sheet", "get_fields", "get_records", "add_fields"}}}
	writeJSONFile(t, localPath, runtime)
	a := gnasFleetBinding{BindingID: "a", PublicResource: "https://a.example", AuthorizationResource: "zoop", Source: "source-a", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-a", RegistryKey: "key-a"}}}
	b := gnasFleetBinding{BindingID: "b", PublicResource: "https://b.example", AuthorizationResource: "zoop", Source: "source-b", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-b", RegistryKey: "key-b"}}}
	d, err := NewGNASHybridDiscovery(filepath.Join(dir, "policy.json"), dir, filepath.Join(dir, "runtime.json"), "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	d.resolveName = func(context.Context, instanceconfig.Config) (string, error) { return "dynamic-b", nil }
	locals := map[string]GNASFleetRuntimeBinding{"a": {BindingID: "a", InstanceConfigPath: localPath, ClientID: "preserved", ClientSecretEnv: "PRESERVED_SECRET"}}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	remote := []gnasFleetBinding{a, b}
	var loadErr error
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		if loadErr != nil {
			return nil, loadErr
		}
		return d.assembleWithLocals(ctx, signedGNASFleetPayload(t, remote), policy, locals)
	}
	loaded, err := load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].Config.Runtime != nil || loaded[0].Config.BoundRuntime == nil || !loaded[0].Config.OAuth21ServiceJWT || loaded[1].Config.Runtime == nil {
		t.Fatal("hybrid config mode incorrect")
	}
	if loaded[0].Config.BoundRuntime.Digest() != runtime.Digest() {
		t.Fatal("static execution identity/capabilities changed")
	}
	if loaded[0].Config.GNASBindingDigest != gnasBindingDigest(a) || loaded[1].Config.GNASBindingDigest != gnasBindingDigest(b) {
		t.Fatal("Binding digest not exact")
	}
	for index, c := range loaded {
		service, err := NewService(c.Config, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, def := range service.definitions {
			names[def.Name] = true
		}
		for _, name := range []string{"wecom_record_apply", "wecom_send_app_message", "wecom_schema_migration_apply", "wecom_instance_initialize"} {
			if names[name] != (index == 0) {
				t.Fatalf("index=%d capability=%s available=%v", index, name, names[name])
			}
		}
		if !names["wecom_record_query"] {
			t.Fatal("reader missing")
		}
	}
	fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(c Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(host string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://"+host+"/mcp", nil)
		r.Header.Set("X-Forwarded-Host", "a.example")
		fleet.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s=%d want %d", host, w.Code, want)
		}
	}
	check("a.example", 200)
	check("b.example", 200)
	check("unknown.example", 421)
	// Duplicated identity/host/source never publishes a partial fleet.
	for _, bad := range []gnasFleetBinding{a, func() gnasFleetBinding { x := b; x.PublicResource = a.PublicResource; return x }(), func() gnasFleetBinding { x := b; x.Source = a.Source; return x }()} {
		remote = []gnasFleetBinding{a, bad}
		if err := fleet.Refresh(t.Context()); err == nil {
			t.Fatal("duplicate accepted")
		}
		check("a.example", 503)
		check("b.example", 503)
	}
	remote = []gnasFleetBinding{a, b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Protected static mapping cannot silently downgrade to a reader on drift.
	drift := runtime
	drift.TenantRoute = "source-other"
	writeJSONFile(t, localPath, drift)
	if err := fleet.Refresh(t.Context()); err == nil {
		t.Fatal("static identity drift accepted")
	}
	check("a.example", 503)
	writeJSONFile(t, localPath, runtime)
	remote = []gnasFleetBinding{a}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 200)
	check("b.example", 421)
	loadErr = errors.New("test outage")
	if err := fleet.Refresh(t.Context()); err == nil {
		t.Fatal("outage accepted")
	}
	check("a.example", 503)
	check("b.example", 421)
	loadErr = nil
	remote = []gnasFleetBinding{b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 421)
	check("b.example", 200)
	remote = []gnasFleetBinding{}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("b.example", 421)
}
