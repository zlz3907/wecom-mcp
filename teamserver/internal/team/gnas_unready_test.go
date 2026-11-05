package team

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestRealDiscoveryReadinessTransitionsDoNotDrainOtherTenants(t *testing.T) {
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
	bindings := []gnasFleetBinding{unreadyVerifierBinding("a"), unreadyVerifierBinding("b")}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	ready := false
	d.resolveName = func(_ context.Context, runtime instanceconfig.Config) (string, error) {
		if runtime.TenantRoute == "source-b" && !ready {
			return "", errors.New("unready")
		}
		return "real-" + runtime.TenantRoute, nil
	}
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		return d.assemble(ctx, signedGNASFleetPayload(t, bindings), policy)
	}
	hold, entered, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	defer release()
	builds, starts := 0, 0
	failWorker := false
	fleet, err := NewDiscoveryFleetWithReadyWorkers(t.Context(), d.listen, load, func(cfg Config) (http.Handler, error) {
		builds++
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/hold" {
				close(entered)
				<-hold
			}
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}, func(_ context.Context, b LoadedFleetBinding) (ReadyWorker, error) {
		starts++
		if b.Config.Runtime.InstanceName != "real-source-b" {
			return nil, errors.New("wrong discovered name")
		}
		if failWorker {
			return nil, errors.New("worker start failed")
		}
		return &fakeReadyWorker{alive: true, status: http.StatusAccepted}, nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := fleet.bindings["b"].Config.Runtime.InstanceName; got != "b" {
		t.Fatalf("unready placeholder=%q", got)
	}
	unreadyBinding := fleet.bindings["b"]
	go func() {
		defer close(finished)
		w := httptest.NewRecorder()
		fleet.ServeHTTP(w, httptest.NewRequest("GET", "https://a.example/hold", nil))
	}()
	<-entered
	refresh := func() {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- fleet.Refresh(t.Context()) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			release()
			<-done
			t.Fatal("readiness transition drained unrelated long request")
		}
		if got := fleetStatus(fleet, "a.example"); got != http.StatusNoContent {
			t.Fatalf("incumbent status=%d", got)
		}
		select {
		case <-finished:
			t.Fatal("unrelated long request was interrupted")
		default:
		}
	}
	ready = true
	refresh()
	if got := fleet.bindings["b"].Config.Runtime.InstanceName; got != "real-source-b" {
		t.Fatalf("ready name=%q", got)
	}
	readyBinding := fleet.bindings["b"]
	if !sameBindingExceptRegistryReadiness(unreadyBinding, readyBinding) || !sameBindingExceptRegistryReadiness(readyBinding, unreadyBinding) {
		t.Fatal("real discovery readiness transition was not isolated")
	}
	for name, mutate := range map[string]func(*LoadedFleetBinding){
		"binding-source": func(v *LoadedFleetBinding) { v.Binding.Source = "other" },
		"authority":      func(v *LoadedFleetBinding) { v.Config.GNASBindingDigest = "changed" },
		"policy": func(v *LoadedFleetBinding) {
			v.Config.Runtime.APIWhitelist = map[string][]string{"read": {"get_records"}}
		},
		"storage":        func(v *LoadedFleetBinding) { v.Config.Runtime.StatePath += "-other" },
		"runtime-source": func(v *LoadedFleetBinding) { v.Config.Runtime.TenantRoute = "other" },
	} {
		changed := readyBinding
		runtime := *readyBinding.Config.Runtime
		changed.Config.Runtime = &runtime
		mutate(&changed)
		if sameBindingExceptRegistryReadiness(unreadyBinding, changed) {
			t.Fatalf("%s drift was masked", name)
		}
	}
	changed := readyBinding
	runtime := *readyBinding.Config.Runtime
	runtime.InstanceName = "different-ready-name"
	changed.Config.Runtime = &runtime
	if sameBindingExceptRegistryReadiness(readyBinding, changed) {
		t.Fatal("ready identity change was masked")
	}
	badPlaceholder := unreadyBinding
	badRuntime := *unreadyBinding.Config.Runtime
	badRuntime.InstanceName = "unexpected-unready-identity"
	badPlaceholder.Config.Runtime = &badRuntime
	if sameBindingExceptRegistryReadiness(badPlaceholder, readyBinding) {
		t.Fatal("unexpected unready identity change was masked")
	}
	staticReady := readyBinding
	staticReady.Config.BoundRuntime = readyBinding.Config.Runtime
	if sameBindingExceptRegistryReadiness(unreadyBinding, staticReady) {
		t.Fatal("static runtime change was masked")
	}
	if got := fleetStatus(fleet, "b.example"); got != http.StatusAccepted || starts != 1 || builds != 1 {
		t.Fatalf("ready status=%d starts=%d builds=%d", got, starts, builds)
	}
	// Force the next genuine discovery to re-probe and lose the Registry.
	delete(d.instances, "b")
	ready = false
	refresh()
	if got := fleet.bindings["b"].Config.Runtime.InstanceName; got != "b" {
		t.Fatalf("unready placeholder=%q", got)
	}
	if got := fleetStatus(fleet, "b.example"); got != http.StatusServiceUnavailable {
		t.Fatalf("unready status=%d", got)
	}
	ready, failWorker = true, true
	refresh()
	if got := fleetStatus(fleet, "b.example"); got != http.StatusServiceUnavailable || builds != 1 || starts != 2 {
		t.Fatalf("failed worker status=%d builds=%d starts=%d", got, builds, starts)
	}
	failWorker = false
	refresh()
	if got := fleetStatus(fleet, "b.example"); got != http.StatusAccepted || builds != 1 || starts != 3 {
		t.Fatalf("worker retry status=%d builds=%d starts=%d", got, builds, starts)
	}
	release()
	<-finished
}

func TestDiscoveryUnreadyHostRecoversAndRevokesWithoutHealthyRebuild(t *testing.T) {
	setupFleetOAuthEnvironment(t)
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "discovery-service")
	t.Setenv("GNAS_APP_SECRET", "test-service-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewGNASDiscovery(filepath.Join(dir, "policy.json"), dir, "127.0.0.1:17801")
	if err != nil {
		t.Fatal(err)
	}
	makeBinding := func(id string) gnasFleetBinding {
		return gnasFleetBinding{BindingID: id, PublicResource: "https://" + id + ".example", AuthorizationResource: "zoop", Source: "source-" + id, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-" + id, RegistryKey: "key-" + id}}}
	}
	a, b := makeBinding("a"), makeBinding("b")
	remote := []gnasFleetBinding{a, b}
	ready := false
	d.resolveName = func(_ context.Context, runtime instanceconfig.Config) (string, error) {
		if runtime.TenantRoute == "source-b" && !ready {
			return "", errors.New("PRIVATE_UPSTREAM_CANARY")
		}
		return "instance-" + runtime.TenantRoute, nil
	}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	var authorityErr error
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		if authorityErr != nil {
			return nil, authorityErr
		}
		return d.assemble(ctx, signedGNASFleetPayload(t, remote), policy)
	}
	builds := map[string]int{}
	fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(cfg Config) (http.Handler, error) {
		builds[cfg.AuthorizationTenant]++
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(host string, want int) {
		t.Helper()
		for _, path := range []string{"/mcp", "/readyz", "/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
			r := httptest.NewRequest(http.MethodPost, "https://"+host+path, strings.NewReader("PRIVATE_REQUEST_CANARY"))
			r.Header.Set("X-Forwarded-Host", "a.example")
			r.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_CANARY")
			w := httptest.NewRecorder()
			fleet.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "CANARY") || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("unexpected route status or response: host=%s path=%s status=%d", host, path, w.Code)
			}
			if want == 503 && authorityErr == nil && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unready response was cacheable")
			}
		}
	}
	check("a.example", 204)
	check("b.example", 503)
	check("unknown.example", 421)
	if builds["a"] != 1 || builds["b"] != 0 {
		t.Fatal("unready service constructed")
	}
	ready = true
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 204)
	if builds["a"] != 1 || builds["b"] != 1 {
		t.Fatal("healthy service rebuilt or recovered service missing")
	}
	// Identity change discards cached readiness and cannot retain the old service.
	ready = false
	b.Plugins.Zoop = &gnasFleetZoopPlugin{RegistryDocumentID: "registry-b-new", RegistryKey: "key-b"}
	remote = []gnasFleetBinding{a, b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 503)
	if builds["a"] != 1 || builds["b"] != 1 {
		t.Fatal("unready replacement constructed")
	}
	authorityErr = errors.New("authority unavailable")
	if err := fleet.Refresh(t.Context()); err == nil {
		t.Fatal("authority outage ignored")
	}
	check("a.example", 503)
	check("b.example", 503)
	check("unknown.example", 421)
	authorityErr = nil
	remote = []gnasFleetBinding{a}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 204)
	check("b.example", 421)
	remote = []gnasFleetBinding{b}
	if err := fleet.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("a.example", 421)
	check("b.example", 503)
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("discovery created tenant assets")
	}
}

func TestDiscoveryReadinessProbeDeadlineLeavesPublicationBudget(t *testing.T) {
	d := &GNASDiscovery{stateRoot: t.TempDir(), instances: map[string]discoveredInstance{}}
	d.resolveName = func(ctx context.Context, runtime instanceconfig.Config) (string, error) {
		if runtime.TenantRoute == "source-fast" {
			return "ready", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	bindings := []gnasFleetBinding{
		{BindingID: "slow", Source: "source-slow", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-slow", RegistryKey: "key-slow"}}},
		{BindingID: "fast", Source: "source-fast", Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-fast", RegistryKey: "key-fast"}}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	resolved := d.resolveRegistryNames(ctx, bindings, DiscoveryPolicy{}, nil)
	if ctx.Err() != nil || len(resolved) != 1 || resolved["fast"].name != "ready" {
		t.Fatal("slow tenant exhausted shared discovery budget")
	}
}

func TestPeriodicRefreshAllowsSlowReadyTenantWithoutInterruptingExisting(t *testing.T) {
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
	binding := func(id string) gnasFleetBinding {
		return gnasFleetBinding{BindingID: id, PublicResource: "https://" + id + ".example", AuthorizationResource: "zoop", Source: "source-" + id, Plugins: gnasFleetPlugins{Zoop: &gnasFleetZoopPlugin{RegistryDocumentID: "registry-" + id, RegistryKey: "key-" + id}}}
	}
	started := make(chan struct{})
	d.resolveName = func(ctx context.Context, runtime instanceconfig.Config) (string, error) {
		switch runtime.TenantRoute {
		case "source-slow":
			close(started)
			select {
			case <-time.After(5300 * time.Millisecond):
				return "ready-slow", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case "source-unready":
			return "", errors.New("unavailable")
		default:
			return "ready-existing", nil
		}
	}
	policy := DiscoveryPolicy{Version: 1, APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	remote := []gnasFleetBinding{binding("existing")}
	load := func(ctx context.Context) ([]LoadedFleetBinding, error) {
		return d.assemble(ctx, signedGNASFleetPayload(t, remote), policy)
	}
	fleet, err := NewDiscoveryFleet(t.Context(), d.listen, load, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	remote = append(remote, binding("slow"), binding("unready"))
	result := make(chan error, 1)
	go func() { result <- fleet.refreshOnce(t.Context()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow discovery did not start")
	}
	check := func(host string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		fleet.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://"+host+"/mcp", nil))
		if w.Code != want {
			t.Fatalf("host %s: got %d want %d", host, w.Code, want)
		}
	}
	check("existing.example", 204)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	check("existing.example", 204)
	check("slow.example", 204)
	check("unready.example", 503)
}
