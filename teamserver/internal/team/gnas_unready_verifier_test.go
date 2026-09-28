package team

import (
	"context"
	"errors"
	"fmt"
	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func unreadyVerifierBinding(id string) gnasFleetBinding {
	return gnasFleetBinding{BindingID: id, PublicResource: "https://" + id + ".example", AuthorizationResource: "zoop", Source: "source-" + id, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-" + id, RegistryKey: "key-" + id}}}
}

func TestUnreadyVerifierMixedCapabilitiesAndCache(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	d := f.discovery
	d.staticRecoveryURL = ""
	f.bindings = append(f.bindings, unreadyVerifierBinding("c"))
	var mu sync.Mutex
	calls := map[string]int{}
	cReady := false
	d.resolveName = func(_ context.Context, c instanceconfig.Config) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[c.TenantRoute]++
		if c.TenantRoute == "source-a" {
			return "", errors.New("local must not be probed")
		}
		if c.TenantRoute == "source-c" && !cReady {
			return "", errors.New("PRIVATE_REGISTRY_CANARY")
		}
		return "dynamic-" + c.TenantRoute, nil
	}
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		return d.assembleWithLocals(ctx, signedGNASFleetPayload(t, f.bindings), f.policy, f.locals)
	}
	builds := map[string]int{}
	fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(c Config) (http.Handler, error) {
		builds[c.AuthorizationTenant]++
		if c.AuthorizationTenant == "a" {
			if c.Runtime != nil || c.BoundRuntime == nil || c.BoundRuntime.Digest() != f.runtime.Digest() || !c.OAuth21ServiceJWT || c.GNASBindingDigest != gnasBindingDigest(f.bindings[0]) {
				t.Error("local capabilities or authoritative identity lost")
			}
		} else if c.Runtime == nil || c.BoundRuntime != nil || c.Runtime.Allows("add_records") || !c.Runtime.Allows("get_records") {
			t.Error("dynamic reader capabilities changed")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(host string, want int) {
		t.Helper()
		for _, p := range []string{"/mcp", "/readyz", "/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server", "/arbitrary"} {
			req := httptest.NewRequest("POST", "https://"+host+p, strings.NewReader("PRIVATE_BODY_CANARY"))
			req.Header.Set("X-Forwarded-Host", "a.example")
			req.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_CANARY")
			w := httptest.NewRecorder()
			fleet.ServeHTTP(w, req)
			if w.Code != want || strings.Contains(w.Body.String(), "CANARY") {
				t.Fatalf("host %s path %s: status=%d want=%d", host, p, w.Code, want)
			}
			if want == 503 && (w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("WWW-Authenticate") != "") {
				t.Fatal("unready response contract changed")
			}
		}
	}
	check("a.example", 204)
	check("b.example", 204)
	check("c.example", 503)
	check("unknown.example", 421)
	if builds["c"] != 0 {
		t.Fatal("unready handler built")
	}
	for i := 0; i < 2; i++ {
		if err := fleet.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := map[string]int{}
	for k, v := range calls {
		got[k] = v
	}
	mu.Unlock()
	if !reflect.DeepEqual(got, map[string]int{"source-b": 1, "source-c": 3}) {
		t.Fatalf("positive/negative cache calls=%v", got)
	}
	mu.Lock()
	cReady = true
	mu.Unlock()
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("c.example", 204)
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["source-c"] != 4 || builds["a"] != 1 || builds["b"] != 1 || builds["c"] != 1 {
		t.Fatalf("recovery/reuse calls=%v builds=%v", calls, builds)
	}
}

func TestUnreadyVerifierGlobalFailuresRemainGlobal(t *testing.T) {
	for _, kind := range []string{"common-policy", "common-auth", "local-identity", "storage-unready", "storage-ready"} {
		t.Run(kind, func(t *testing.T) {
			f := newStaticRecoveryFixture(t)
			d := f.discovery
			d.staticRecoveryURL = ""
			f.bindings = append(f.bindings, unreadyVerifierBinding("c"))
			d.resolveName = func(_ context.Context, c instanceconfig.Config) (string, error) {
				if c.TenantRoute == "source-c" {
					return "", errors.New("unready")
				}
				return "ready-" + c.TenantRoute, nil
			}
			load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
				return d.assembleWithLocals(ctx, signedGNASFleetPayload(t, f.bindings), f.policy, f.locals)
			}
			builds := 0
			fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(Config) (http.Handler, error) {
				builds++
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "common-policy":
				f.policy.APIWhitelist = map[string][]string{"read": {"not_supported"}}
			case "common-auth":
				t.Setenv("TEAM_MCP_AUTH_MODE", "invalid")
			case "local-identity":
				f.runtime.RegistryKey = "wrong-key"
				writeJSONFile(t, f.localPath, f.runtime)
			case "storage-unready":
				f.runtime.StatePath = d.runtimeFor(f.bindings[2], f.policy).StatePath
				writeJSONFile(t, f.localPath, f.runtime)
			case "storage-ready":
				f.runtime.StatePath = d.runtimeFor(f.bindings[1], f.policy).StatePath
				writeJSONFile(t, f.localPath, f.runtime)
			}
			if err := fleet.Refresh(t.Context()); err == nil {
				t.Fatal("global invalid configuration accepted")
			}
			if builds != 2 {
				t.Fatal("failed refresh constructed handlers")
			}
			for _, host := range []string{"a.example", "b.example", "c.example", "unknown.example"} {
				w := httptest.NewRecorder()
				fleet.ServeHTTP(w, httptest.NewRequest("GET", "https://"+host+"/mcp", nil))
				want := 503
				if host == "unknown.example" {
					want = 421
				}
				if w.Code != want {
					t.Fatalf("%s status=%d want=%d", host, w.Code, want)
				}
			}
		})
	}
}

func TestUnreadyVerifierConcurrentBoundedColdProbes(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	d := f.discovery
	d.staticRecoveryURL = ""
	bindings := make([]gnasFleetBinding, 64)
	for i := range bindings {
		bindings[i] = unreadyVerifierBinding(fmt.Sprintf("tenant-%02d", i))
	}
	started := make(chan string, 64)
	release := make(chan struct{})
	var probes atomic.Int64
	d.resolveName = func(ctx context.Context, c instanceconfig.Config) (string, error) {
		probes.Add(1)
		started <- c.TenantRoute
		select {
		case <-release:
			return c.TenantRoute, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	payload := signedGNASFleetPayload(t, bindings)
	done := make(chan error, 1)
	go func() {
		loaded, err := d.assemble(ctx, payload, f.policy)
		if err == nil && len(loaded) != 64 {
			err = fmt.Errorf("loaded=%d", len(loaded))
		}
		done <- err
	}()
	seen := map[string]bool{}
	for range bindings {
		select {
		case id := <-started:
			if seen[id] {
				t.Fatal("duplicate probe")
			}
			seen[id] = true
		case err := <-done:
			t.Fatalf("returned before all probes started: %v", err)
		case <-ctx.Done():
			t.Fatal("all 64 cold probes did not start concurrently")
		}
	}
	// Release only after every probe entered: sequential implementation cannot pass.
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	tooMany := append(append([]gnasFleetBinding{}, bindings...), unreadyVerifierBinding("tenant-64"))
	before := probes.Load()
	if _, err := d.assemble(t.Context(), signedGNASFleetPayload(t, tooMany), f.policy); err == nil {
		t.Fatal("65 bindings accepted")
	}
	if probes.Load() != before {
		t.Fatal("oversized authority performed probes")
	}
}

func TestUnreadyVerifierCancellationDoesNotPublishOrCache(t *testing.T) {
	f := newStaticRecoveryFixture(t)
	d := f.discovery
	d.staticRecoveryURL = ""
	var calls atomic.Int64
	d.resolveName = func(context.Context, instanceconfig.Config) (string, error) { calls.Add(1); return "cached-ready", nil }
	if _, err := d.assembleWithLocals(t.Context(), signedGNASFleetPayload(t, f.bindings), f.policy, f.locals); err != nil {
		t.Fatal(err)
	}
	original := d.instances["b"]
	newBindings := append(append([]gnasFleetBinding{}, f.bindings...), unreadyVerifierBinding("c"))
	entered := make(chan struct{})
	d.resolveName = func(ctx context.Context, c instanceconfig.Config) (string, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return "late-name", nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	payload := signedGNASFleetPayload(t, newBindings)
	go func() { _, err := d.assembleWithLocals(ctx, payload, f.policy, f.locals); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("new probe never entered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if len(d.instances) != 1 || !reflect.DeepEqual(d.instances["b"], original) {
		t.Fatal("canceled refresh changed ready cache")
	}
	d.resolveName = func(context.Context, instanceconfig.Config) (string, error) { calls.Add(1); return "recovered", nil }
	got, err := d.assembleWithLocals(t.Context(), payload, f.policy, f.locals)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].RegistryUnavailable || calls.Load() != 3 {
		t.Fatal("canceled result cached or ready cache lost")
	}
}
