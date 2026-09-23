package team

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestHybridVerifierHTTPToolsAndConfigDrift(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "test-service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir := t.TempDir()
	path := filepath.Join(dir, "static.json")
	runtime := instanceconfig.Config{Version: 1, InstanceName: "static-a", TenantRoute: "source-a", RegistryDocumentID: "registry-a", RegistryKey: "key-a", StatePath: filepath.Join(dir, "static-state.json"), WecomOperatorUserID: "operator-a", AIExecutionSubjectRecordID: "executor-a", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	writeJSONFile(t, path, runtime)
	newServer := func(dynamic bool) *httptest.Server {
		t.Helper()
		fixed := runtime
		overrides := BindingOverrides{BoundRuntime: &fixed, OAuth21ServiceJWT: true, GNASBindingDigest: strings.Repeat("a", 64), PublicURL: "https://mcp.example.test", OIDCIssuer: "https://mcp.example.test/gnas/oauth", AuthorizationTenant: "binding-a", AuthorizationResource: "zoop", Plugins: []string{"zoop"}}
		configPath := path
		if dynamic {
			fixed.InstanceName, fixed.TenantRoute = "dynamic-b", "source-b"
			fixed.RegistryDocumentID, fixed.RegistryKey = "registry-b", "key-b"
			fixed.StatePath = filepath.Join(dir, "dynamic-state.json")
			fixed.WecomOperatorUserID, fixed.AIExecutionSubjectRecordID = "", ""
			overrides.Runtime, overrides.BoundRuntime = &fixed, nil
			overrides.AuthorizationTenant = "binding-b"
			configPath = ""
		}
		cfg, err := LoadConfigForBinding(configPath, "127.0.0.1:17801", overrides)
		if err != nil {
			t.Fatal(err)
		}
		cfg.TrustedLoopbackProxy = true
		service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
			return &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Minute), UserID: "test-employee", Scopes: cfg.RequiredScopes, Extra: map[string]any{"role": string(RolePolicy), "issuer": cfg.OIDCIssuer, "wecom_userid": "test-employee", "mcp_role": "admin", "effective_tools": []string{"*"}}}, nil
		}
		server := httptest.NewServer(service.Handler(verifier))
		t.Cleanup(server.Close)
		return server
	}
	static, dynamic := newServer(false), newServer(true)
	staticTools, dynamicTools := listTools(t, static.URL, "test-verified"), listTools(t, dynamic.URL, "test-verified")
	for _, name := range []string{"wecom_record_apply", "wecom_send_app_message", "wecom_schema_migration_apply", "wecom_instance_initialize", "wecom_instance_initialize_status"} {
		if !staticTools[name] || dynamicTools[name] {
			t.Fatalf("tool catalog isolation failed: %s static=%v dynamic=%v", name, staticTools[name], dynamicTools[name])
		}
	}
	if !staticTools["wecom_record_query"] || !dynamicTools["wecom_record_query"] {
		t.Fatal("reader query omitted")
	}
	checkGet := func(server *httptest.Server, path string, want int) {
		t.Helper()
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s status=%d want=%d", path, response.StatusCode, want)
		}
	}
	checkGet(static, "/readyz", 200)
	changed := runtime
	changed.TenantRoute = "unexpected-source"
	writeJSONFile(t, path, changed)
	for _, route := range []string{"/healthz", "/readyz", "/.well-known/oauth-protected-resource/mcp"} {
		checkGet(static, route, 503)
		checkGet(dynamic, route, 200)
	}
	response := postRPC(t, static.URL+"/mcp", "test-verified", `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("changed local instance tools/list status=%d", response.StatusCode)
	}
	if !listTools(t, dynamic.URL, "test-verified")["wecom_record_query"] {
		t.Fatal("static drift disrupted dynamic reader")
	}
	writeJSONFile(t, path, runtime)
	checkGet(static, "/readyz", 200)
	if !listTools(t, static.URL, "test-verified")["wecom_record_apply"] {
		t.Fatal("restored bound instance did not recover")
	}
}

func TestHybridVerifierRejectsCrossTenantStorageAliases(t *testing.T) {
	for _, scenario := range []string{"valid", "same_name", "same_state", "schema_over_state", "config_over_state", "noncanonical", "symlink_parent"} {
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			a := instanceconfig.Config{InstanceName: "a", StatePath: filepath.Join(root, "a.json")}
			b := instanceconfig.Config{InstanceName: "b", StatePath: filepath.Join(root, "b.json")}
			loaded := []LoadedFleetBinding{
				{Config: Config{BoundRuntime: &a, InstanceConfigPath: filepath.Join(root, "a-config.json")}},
				{Config: Config{Runtime: &b}},
			}
			switch scenario {
			case "same_name":
				b.InstanceName = a.InstanceName
			case "same_state":
				b.StatePath = a.StatePath
			case "schema_over_state":
				b.SchemaMirrorPath = a.StatePath
			case "config_over_state":
				loaded[0].Config.InstanceConfigPath = b.StatePath
			case "noncanonical":
				b.StatePath = root + "/./b.json"
			case "symlink_parent":
				real := filepath.Join(root, "real")
				if err := os.Mkdir(real, 0700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(real, alias); err != nil {
					t.Skip("symlinks unavailable")
				}
				b.StatePath = filepath.Join(alias, "state.json")
			}
			if err := validateHybridStorage(loaded); (err == nil) != (scenario == "valid") {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
		})
	}
}

func TestHybridVerifierMissingMappedInstanceNeverFallsBack(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewGNASHybridDiscovery(filepath.Join(root, "policy.json"), root, filepath.Join(root, "runtime.json"), "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	d.resolveName = func(context.Context, instanceconfig.Config) (string, error) {
		t.Fatal("missing protected instance downgraded to dynamic discovery")
		return "", nil
	}
	remote := gnasFleetBinding{BindingID: "a", Source: "source-a", PublicResource: "https://a.example", AuthorizationResource: "zoop", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-a", RegistryKey: "key-a"}}}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	locals := map[string]GNASFleetRuntimeBinding{"a": {BindingID: "a", InstanceConfigPath: filepath.Join(root, "missing.json")}}
	if _, err := d.assembleWithLocals(t.Context(), signedGNASFleetPayload(t, []gnasFleetBinding{remote}), policy, locals); err == nil {
		t.Fatal("missing protected instance accepted")
	}
	if len(d.instances) != 0 {
		t.Fatal("failed mapped instance published partial state")
	}
}
