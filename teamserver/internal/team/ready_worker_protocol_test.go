package team

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"testing"
)

func workerProtocolBinding() LoadedFleetBinding {
	return LoadedFleetBinding{
		Binding: FleetBinding{BindingID: "tenant_one", Hosts: []string{"one.example"}, PublicURL: "https://one.example", AuthorizationTenant: "tenant_one", AuthorizationResource: "zoop"},
		Config:  Config{GNASBindingDigest: strings.Repeat("a", 64), AuthorizationTenant: "tenant_one", AuthorizationResource: "zoop", PublicURL: "https://one.example"},
	}
}

func signedWorkerReady(t *testing.T, secret []byte, spec BindingWorkerSpec) BindingWorkerReady {
	t.Helper()
	ready := BindingWorkerReady{Version: BindingWorkerProtocolVersion, BindingDigest: spec.BindingDigest, Tenant: spec.Tenant, Resource: spec.Resource, Address: "127.0.0.1:43210"}
	mac, err := SignBindingWorkerReady(secret, ready)
	if err != nil {
		t.Fatal(err)
	}
	ready.MAC = mac
	return ready
}

func TestBindingWorkerReadyHandshakeBindsDigestTenantResourceAndLoopback(t *testing.T) {
	spec, err := NewBindingWorkerSpec(workerProtocolBinding())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("s", 32))
	ready := signedWorkerReady(t, secret, spec)
	if err := VerifyBindingWorkerReady(secret, spec, ready); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*BindingWorkerReady){
		"digest":   func(r *BindingWorkerReady) { r.BindingDigest = strings.Repeat("b", 64) },
		"tenant":   func(r *BindingWorkerReady) { r.Tenant = "tenant_two" },
		"resource": func(r *BindingWorkerReady) { r.Resource = "other" },
		"address":  func(r *BindingWorkerReady) { r.Address = "127.0.0.2:43210" },
		"mac":      func(r *BindingWorkerReady) { r.MAC = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := ready
			mutate(&bad)
			if err := VerifyBindingWorkerReady(secret, spec, bad); err == nil {
				t.Fatal("tampered worker ready response was accepted")
			}
		})
	}
}

func TestBindingWorkerProtocolRejectsControlsAndNonLoopback(t *testing.T) {
	spec, err := NewBindingWorkerSpec(workerProtocolBinding())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("s", 32))
	for name, mutate := range map[string]func(*BindingWorkerReady){
		"leading control":  func(r *BindingWorkerReady) { r.Tenant = "\t" + r.Tenant },
		"interior control": func(r *BindingWorkerReady) { r.Resource = "zo\nop" },
		"trailing control": func(r *BindingWorkerReady) { r.Address += "\r" },
		"public address":   func(r *BindingWorkerReady) { r.Address = "192.0.2.1:43210" },
	} {
		t.Run(name, func(t *testing.T) {
			ready := signedWorkerReady(t, secret, spec)
			mutate(&ready)
			if err := VerifyBindingWorkerReady(secret, spec, ready); err == nil {
				t.Fatal("unsafe ready response was accepted")
			}
		})
	}
	bad := workerProtocolBinding()
	bad.Binding.PublicURL = "https://one.example\n"
	bad.Config.PublicURL = bad.Binding.PublicURL
	if _, err := NewBindingWorkerSpec(bad); err == nil {
		t.Fatal("control character in worker spec was accepted")
	}
}

func TestBindingWorkerSpecRejectsAuthorityDrift(t *testing.T) {
	bad := workerProtocolBinding()
	bad.Config.AuthorizationResource = "other"
	if _, err := NewBindingWorkerSpec(bad); err == nil {
		t.Fatal("authority resource drift was accepted")
	}
}

func TestReadyWorkerReconstructsAdminOnlyForExactBinding(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "test-app")
	t.Setenv("GNAS_APP_SECRET", "test-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	t.Setenv("GNAS_SCHEMA_ADMIN_BINDING_DIGEST", strings.Repeat("a", 64))
	t.Setenv("GNAS_SCHEMA_ADMIN_USERID", "verified-member")
	runtime := instanceconfig.Config{Version: 1, InstanceName: "tenant_one", TenantRoute: "source-one", RegistryDocumentID: "registry-one", RegistryKey: "key-one", SchemaSource: "z-s00", StatePath: filepath.Join(t.TempDir(), "state.json"), APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}, OAuthSchemaAdminUserID: "verified-member"}
	start := BindingWorkerStart{Identity: BindingWorkerSpec{Version: BindingWorkerProtocolVersion, BindingID: "tenant_one", BindingDigest: strings.Repeat("a", 64), Tenant: "tenant_one", Resource: "zoop", PublicURL: "https://one.example"}, Runtime: &runtime}
	wire, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte("verified-member")) {
		t.Fatal("internal grant serialized over worker pipe")
	}
	var decoded BindingWorkerStart
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	cfg, err := decoded.LoadConfig("127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime == nil || cfg.Runtime.OAuthSchemaAdminUserID != "verified-member" {
		t.Fatal("matching worker lost admin grant")
	}
	decoded.Identity.BindingDigest = strings.Repeat("b", 64)
	cfg, err = decoded.LoadConfig("127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime == nil || cfg.Runtime.OAuthSchemaAdminUserID != "" {
		t.Fatal("another worker received the grant")
	}
}
