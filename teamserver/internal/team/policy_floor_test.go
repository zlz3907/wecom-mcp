package team

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func writePolicyFloorFixture(t *testing.T, dir, key, revision string) string {
	t.Helper()
	path := filepath.Join(dir, key+".json")
	data, err := json.Marshal(policyFloorDocument{SchemaVersion: 1, BindingKey: key, Revision: revision, Generation: "generation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0400); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".lock", nil, 0400); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicyFloorSharedBindingVector(t *testing.T) {
	key := policyFloorBindingKey("app", "tenant", "zoop", "https://tenant.example/mcp", "https://tenant.example/gnas/oauth")
	if key != "48ea9ac785a48e4c66165857216a0a3ed307b4dce25cfff0ac668deec13a3f42" {
		t.Fatalf("cross-service floor key mismatch: %q", key)
	}

	key = policyFloorBindingKey("app", "tenant", "zoop", "https://TENANT.EXAMPLE/Space/mcp", "https://TENANT.EXAMPLE/gnas/oauth")
	if key != "09476f216209bef10c3a3edc317523b18a62a9d3d5103fc4a59919409486ad73" {
		t.Fatalf("namespace floor key mismatch: %q", key)
	}
}

func TestPolicyFloorLegacyNamespaceAndIssuerIsolation(t *testing.T) {
	legacy := policyFloorBindingKey("", "tenant", "zoop", "http://127.0.0.1:17801/mcp", "https://login.example/issuer")
	if legacy == "" {
		t.Fatal("legacy loopback OAuth floor rejected")
	}
	if key := policyFloorBindingKey("~legacy", "tenant", "zoop", "http://127.0.0.1:17801/mcp", "https://login.example/issuer"); key != "" {
		t.Fatal("reserved legacy namespace accepted as an app ID")
	}
	if other := policyFloorBindingKey("", "tenant", "zoop", "http://127.0.0.1:17801/mcp", "https://other.example/issuer"); other == "" || other == legacy {
		t.Fatal("different issuers share a floor")
	}
	if key := policyFloorBindingKey("app", "tenant", "zoop", "https://tenant.example:443/mcp", "https://login.example/issuer"); key == "" {
		t.Fatal("explicit HTTPS port rejected")
	}
}

func TestPolicyFloorKeepsOtherTenantsOpenAndPinsRevision(t *testing.T) {
	dir := t.TempDir()
	first := policyFloorBindingKey("app", "tenant-a", "zoop", "https://a.example/mcp", "https://a.example/gnas/oauth")
	second := policyFloorBindingKey("app", "tenant-b", "zoop", "https://b.example/mcp", "https://b.example/gnas/oauth")
	writePolicyFloorFixture(t, dir, first, "revision-1")
	writePolicyFloorFixture(t, dir, second, "")
	for _, tc := range []struct {
		key      string
		expected policyFloorDecision
	}{{first, policyFloorDecision{Required: true, Revision: "revision-1", Generation: "generation-1"}}, {second, policyFloorDecision{Generation: "generation-1"}}} {
		decision, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), tc.key)
		if err != nil || decision != tc.expected || release == nil {
			t.Fatalf("unexpected floor: %+v %v", decision, err)
		}
		release()
	}
}

func TestPolicyFloorFailsClosedForUntrustedOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	key := policyFloorBindingKey("app", "tenant", "zoop", "https://a.example/mcp", "https://a.example/gnas/oauth")
	path := writePolicyFloorFixture(t, dir, key, "revision-1")
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), key); err == nil || release != nil {
		t.Fatal("writable floor accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	invalid := `{"schema_version":1,"binding_key":"` + key + `","revision":"revision-1","revision":""}`
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), key); err == nil || release != nil {
		t.Fatal("duplicate floor field accepted")
	}
	invalid = `{"schema_version":1,"binding_key":"` + key + `","revision":null,"generation":"g1"}`
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), key); err == nil || release != nil {
		t.Fatal("null revision accepted as open")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".lock", path); err != nil {
		t.Fatal(err)
	}
	if _, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), key); err == nil || release != nil {
		t.Fatal("symlink floor accepted")
	}
}

func TestPolicyFloorPerTenantLockIsolation(t *testing.T) {
	dir := t.TempDir()
	first := policyFloorBindingKey("app", "tenant-a", "zoop", "https://a.example/mcp", "https://a.example/gnas/oauth")
	second := policyFloorBindingKey("app", "tenant-b", "zoop", "https://b.example/mcp", "https://b.example/gnas/oauth")
	firstPath := writePolicyFloorFixture(t, dir, first, "")
	writePolicyFloorFixture(t, dir, second, "")
	lock, err := os.Open(firstPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, release, err := acquirePolicyFloor(ctx, dir, dir, uint32(os.Getuid()), first); err == nil || release != nil {
		t.Fatal("activation lock ignored")
	}
	if _, release, err := acquirePolicyFloor(context.Background(), dir, dir, uint32(os.Getuid()), second); err != nil || release == nil {
		t.Fatalf("unrelated tenant blocked: %v", err)
	} else {
		release()
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
}

func TestPolicyFloorRejectsRollbackAndWrongRevision(t *testing.T) {
	cfg := testOAuth21Config("https://unused.invalid/introspect")
	cfg.RequiredScopes = nil
	managed := true
	response := oauth21IntrospectionResponse{Active: true, Issuer: cfg.OIDCIssuer, Subject: "employee:member", Audience: json.RawMessage(`"https://mcp.jyiai.com/gmzoop/mcp"`), ExpiresAt: time.Now().Add(time.Minute).Unix(), TokenUse: "access", Tenant: cfg.AuthorizationTenant, WeComUserID: "member", MCPRole: "member", ManagedToolPolicy: &managed, PolicyContractV1: true, PolicyRevision: "revision-1", EffectiveTools: []string{"wecom_record_query"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	cfg.OAuth21IntrospectionURL = server.URL
	verifier, err := NewOAuth21IntrospectionAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	check := func(floor policyFloorDecision, accept bool) {
		t.Helper()
		ctx := context.WithValue(context.Background(), policyFloorContextKey{}, floor)
		_, err := verifier.Verify(ctx, "opaque-token", nil)
		if (err == nil) != accept {
			t.Fatalf("floor=%+v accept=%v err=%v", floor, accept, err)
		}
	}
	check(policyFloorDecision{Required: true, Revision: "revision-1"}, true)
	check(policyFloorDecision{Required: true, Revision: "revision-2"}, false)
	check(policyFloorDecision{}, false)
	managed = false
	response.PolicyContractV1 = false
	response.PolicyRevision = ""
	response.EffectiveTools = []string{"*"}
	check(policyFloorDecision{}, true)
	check(policyFloorDecision{Required: true, Revision: "revision-1"}, false)
}

func TestPolicyToolRevalidationRejectsFloorChangeBeforeDecision(t *testing.T) {
	dir := t.TempDir()
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.AuthorizationServiceAppID, cfg.AuthorizationTenant, cfg.AuthorizationResource = "app", "tenant", "zoop"
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := policyFloorBindingKey(policyFloorAppID(cfg), "tenant", "zoop", cfg.MCPURL, cfg.OIDCIssuer)
	path := writePolicyFloorFixture(t, dir, key, "revision-1")
	prior := requestAuthorization{decision: AuthorizationDecision{UserID: "member", Tenant: "tenant", Resource: "zoop"}}
	tokenInfo := &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Minute), UserID: "member", Extra: map[string]any{"wecom_userid": "member", "mcp_role": "member", "effective_tools": []string{"wecom_schema_status"}, "tool_policy_managed": true, "tool_policy_contract_v1": true, "tool_policy_revision": "revision-1"}}
	verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) { return tokenInfo, nil }
	got, err := svc.revalidatePolicyTool(context.Background(), verifier, "opaque", nil, "wecom_schema_status", prior, dir, dir, uint32(os.Getuid()))
	if err != nil || !got.tools["wecom_schema_status"] {
		t.Fatalf("valid policy rejected: %v", err)
	}
	verifier = func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
		data, _ := json.Marshal(policyFloorDocument{SchemaVersion: 1, BindingKey: key, Revision: "revision-2", Generation: "generation-2"})
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return tokenInfo, nil
	}
	if _, err = svc.revalidatePolicyTool(context.Background(), verifier, "opaque", nil, "wecom_schema_status", prior, dir, dir, uint32(os.Getuid())); err == nil {
		t.Fatal("accepted policy generation changed during introspection")
	}
}

// Legacy SDK tests use a real temporary floor rather than bypassing the guard.
func installTestPolicyFloor(t *testing.T, svc *Service, revision string) {
	t.Helper()
	dir := t.TempDir()
	key := policyFloorBindingKey(policyFloorAppID(svc.config), svc.config.AuthorizationTenant, svc.config.AuthorizationResource, svc.config.MCPURL, svc.config.OIDCIssuer)
	if key == "" {
		t.Fatal("invalid floor test binding")
	}
	writePolicyFloorFixture(t, dir, key, revision)
	svc.config.PolicyFloorPath = dir
	svc.policyFloorRoot = dir
	svc.policyFloorOwner = uint32(os.Getuid())
}
func setTestPolicyFloor(t *testing.T, svc *Service, revision, generation string) {
	t.Helper()
	key := policyFloorBindingKey(policyFloorAppID(svc.config), svc.config.AuthorizationTenant, svc.config.AuthorizationResource, svc.config.MCPURL, svc.config.OIDCIssuer)
	path := filepath.Join(svc.config.PolicyFloorPath, key+".json")
	data, err := json.Marshal(policyFloorDocument{SchemaVersion: 1, BindingKey: key, Revision: revision, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0400); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthServiceHonorsExplicitPolicyFloor(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.PolicyFloorPath = productionPolicyFloorPath
	cfg.AuthorizationServiceAppID, cfg.AuthorizationTenant, cfg.AuthorizationResource = "app", "tenant", "zoop"
	svc, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if svc.config.PolicyFloorPath != productionPolicyFloorPath {
		t.Fatal("OAuth ignored configured policy floor")
	}
	// A missing per-tenant floor fails before token verification; it never
	// converts an unavailable floor into an unrestricted user grant.
	svc.config.PolicyFloorPath = t.TempDir()
	svc.policyFloorRoot = svc.config.PolicyFloorPath
	svc.policyFloorOwner = uint32(os.Getuid())
	called := false
	verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) {
		called = true
		return nil, nil
	}
	unauthenticated := httptest.NewRecorder()
	svc.Handler(verifier).ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if unauthenticated.Code != http.StatusUnauthorized || called {
		t.Fatalf("missing bearer must retain OAuth challenge: status=%d verified=%v", unauthenticated.Code, called)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer test")
	out := httptest.NewRecorder()
	svc.Handler(verifier).ServeHTTP(out, req)
	if out.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("missing floor did not fail closed: status=%d verified=%v", out.Code, called)
	}
	ready := httptest.NewRecorder()
	svc.ready(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing floor must fail readiness: status=%d", ready.Code)
	}
	installTestPolicyFloor(t, svc, "")
	// Handler validates a bound runtime before invoking readiness. This
	// fixture isolates the floor check from that separate runtime check.
	svc.config.BoundRuntime = &instanceconfig.Config{}
	ready = httptest.NewRecorder()
	svc.ready(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("provisioned floor must not block readiness: status=%d", ready.Code)
	}
}

func TestOAuthToolRevalidatesWithoutPolicyFloor(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.AuthorizationTenant, cfg.AuthorizationResource = "tenant", "zoop"
	svc, err := NewService(cfg, nil)
	if err != nil || svc.config.PolicyFloorPath != "" {
		t.Fatalf("OAuth should not require an additional local file: service=%v err=%v", svc != nil, err)
	}
	prior := requestAuthorization{decision: AuthorizationDecision{UserID: "member", Tenant: "tenant", Resource: "zoop"}}
	info := &sdkauth.TokenInfo{Expiration: time.Now().Add(time.Minute), UserID: "member", Extra: map[string]any{
		"wecom_userid": "member", "mcp_role": "member", "effective_tools": []string{"wecom_schema_status"},
		"tool_policy_managed": true, "tool_policy_contract_v1": true, "tool_policy_revision": "revision-1",
	}}
	calls := 0
	verifier := func(context.Context, string, *http.Request) (*sdkauth.TokenInfo, error) { calls++; return info, nil }
	got, err := svc.revalidatePolicyTool(context.Background(), verifier, "opaque", nil, "wecom_schema_status", prior, "", "/", 0)
	if err != nil || !got.tools["wecom_schema_status"] || calls != 1 {
		t.Fatalf("valid current grant rejected: calls=%d err=%v", calls, err)
	}
	info.Extra["effective_tools"] = []string{}
	if _, err := svc.revalidatePolicyTool(context.Background(), verifier, "opaque", nil, "wecom_schema_status", prior, "", "/", 0); err == nil || calls != 2 {
		t.Fatal("revoked tool was not denied using fresh introspection")
	}
	info.Extra["effective_tools"] = []string{"wecom_schema_status"}
	info.Extra["wecom_userid"] = "another-member"
	if _, err := svc.revalidatePolicyTool(context.Background(), verifier, "opaque", nil, "wecom_schema_status", prior, "", "/", 0); err == nil || calls != 3 {
		t.Fatal("changed employee identity was not denied")
	}
}
