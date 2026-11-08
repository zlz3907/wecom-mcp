package team

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func refreshTestBinding(id string) LoadedFleetBinding {
	return LoadedFleetBinding{
		Binding: FleetBinding{BindingID: id, Hosts: []string{id + ".example"}},
		Config:  Config{AuthorizationTenant: id, ListenAddress: "127.0.0.1:17801"},
	}
}

type countingFleetHandler struct{ closed int }

func (h *countingFleetHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
func (h *countingFleetHandler) Close() { h.closed++ }

func TestRefreshingFleetClosesUnpublishedAndShutdownHandlers(t *testing.T) {
	loaded := []LoadedFleetBinding{refreshTestBinding("a")}
	created := map[string]*countingFleetHandler{}
	f, err := NewRefreshingFleet(t.Context(), func(context.Context) ([]LoadedFleetBinding, error) { return loaded, nil }, func(cfg Config) (http.Handler, error) {
		if cfg.AuthorizationTenant == "c" {
			return nil, errors.New("fixture build failure")
		}
		h := &countingFleetHandler{}
		created[cfg.AuthorizationTenant] = h
		return h, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded = []LoadedFleetBinding{refreshTestBinding("a"), refreshTestBinding("b"), refreshTestBinding("c")}
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("invalid generation accepted")
	}
	if created["b"].closed != 1 || created["a"].closed != 0 {
		t.Fatal("unpublished or serving handler lifetime is wrong")
	}
	if err := f.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if created["a"].closed != 1 {
		t.Fatal("serving reporter not stopped on shutdown")
	}
}

func fleetStatus(handler http.Handler, host string) int {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/readyz", nil)
	r.Header.Set("X-Forwarded-Host", "a.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w.Code
}

func TestRefreshingFleetPublishesCompleteGenerationAndRecovers(t *testing.T) {
	a, b := refreshTestBinding("a"), refreshTestBinding("b")
	loaded := []LoadedFleetBinding{a}
	var loadErr error
	builds := 0
	failBuild := false
	f, err := NewRefreshingFleet(t.Context(), func(context.Context) ([]LoadedFleetBinding, error) {
		return loaded, loadErr
	}, func(cfg Config) (http.Handler, error) {
		builds++
		if failBuild && cfg.AuthorizationTenant == "b" {
			return nil, errors.New("test handler failure")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != cfg.AuthorizationTenant+".example" {
				t.Error("request crossed tenant handler boundary")
			}
			w.WriteHeader(http.StatusNoContent)
		}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatus := func(host string, want int) {
		t.Helper()
		if got := fleetStatus(f, host); got != want {
			t.Fatalf("host=%s status=%d want=%d", host, got, want)
		}
	}
	assertStatus("a.example", 204)
	assertStatus("b.example", 421)
	assertStatus("unknown.example", 421)
	if err := f.Refresh(t.Context()); err != nil || builds != 1 {
		t.Fatal("unchanged binding was rebuilt")
	}
	loaded = []LoadedFleetBinding{a, b}
	failBuild = true
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("partial generation accepted")
	}
	assertStatus("a.example", 503)
	assertStatus("b.example", 421)
	failBuild = false
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertStatus("a.example", 204)
	assertStatus("b.example", 204)
	loadErr = errors.New("test resolver failure")
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("resolver failure ignored")
	}
	assertStatus("a.example", 503)
	assertStatus("b.example", 503)
	assertStatus("unknown.example", 421)
	loadErr = nil
	loaded = []LoadedFleetBinding{b}
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertStatus("a.example", 421)
	assertStatus("b.example", 204)
	loaded[0].Config.ListenAddress = "127.0.0.1:19999"
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("listener drift accepted")
	}
	assertStatus("b.example", 503)
}

func TestRefreshingFleetConcurrentRequestsAndCancellation(t *testing.T) {
	loaded := []LoadedFleetBinding{refreshTestBinding("a")}
	f, err := NewRefreshingFleet(t.Context(), func(context.Context) ([]LoadedFleetBinding, error) {
		return loaded, nil
	}, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 100; i++ {
				if status := fleetStatus(f, "a.example"); status != 204 && status != 503 {
					t.Error("existing route lost during refresh or drain")
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		loaded = []LoadedFleetBinding{refreshTestBinding("a"), refreshTestBinding("b")}
		if i%2 == 0 {
			loaded = loaded[:1]
		}
		if err := f.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.Refresh(ctx); err == nil || fleetStatus(f, "a.example") != 503 {
		t.Fatal("canceled generation published")
	}
}

func TestRefreshingFleetDrainsOldRequestsBeforeReplacement(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		remove bool
		cancel bool
	}{{"replace", false, false}, {"cancel_replace", false, true}, {"remove_readd", true, false}, {"cancel_remove", true, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			loaded := []LoadedFleetBinding{refreshTestBinding("a")}
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			f, err := NewRefreshingFleet(t.Context(), func(context.Context) ([]LoadedFleetBinding, error) {
				return loaded, nil
			}, func(cfg Config) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if cfg.PublicURL == "" {
						close(entered)
						<-release
						w.WriteHeader(201)
						return
					}
					w.WriteHeader(202)
				}), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			oldDone := make(chan int, 1)
			go func() { oldDone <- fleetStatus(f, "a.example") }()
			<-entered
			// Replace the config without mutating slices retained by the old generation.
			loaded = []LoadedFleetBinding{refreshTestBinding("a")}
			loaded[0].Config.PublicURL = "https://a.example"
			if scenario.remove {
				loaded = []LoadedFleetBinding{refreshTestBinding("b")}
				loaded[0].Config.PublicURL = "https://b.example"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			refreshed := make(chan error, 1)
			go func() { refreshed <- f.Refresh(ctx) }()
			deadline := time.Now().Add(5 * time.Second)
			for !f.current.Load().unavailable && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !f.current.Load().unavailable || fleetStatus(f, "a.example") != 503 {
				t.Fatal("new requests admitted while old generation was executing")
			}
			select {
			case <-refreshed:
				t.Fatal("replacement published before old request finished")
			default:
			}
			if scenario.cancel {
				cancel()
				if err := <-refreshed; err == nil || fleetStatus(f, "a.example") != 503 {
					t.Fatal("canceled drain did not fail closed")
				}
			}
			unblock()
			if status := <-oldDone; status != 201 {
				t.Fatal("old request did not finish on its original handler")
			}
			if scenario.cancel {
				err = f.Refresh(t.Context())
			} else {
				err = <-refreshed
			}
			if scenario.remove {
				if err != nil || fleetStatus(f, "a.example") != 421 {
					t.Fatal("removed handler remained available")
				}
				loaded = []LoadedFleetBinding{refreshTestBinding("a")}
				loaded[0].Config.PublicURL = "https://a.example"
				err = f.Refresh(t.Context())
			}
			if err != nil || fleetStatus(f, "a.example") != 202 {
				t.Fatal("drained generation did not switch to the new handler")
			}
		})
	}
}

func TestRefreshingFleetRunPollsAndStops(t *testing.T) {
	loads := 0
	f, err := NewRefreshingFleet(t.Context(), func(context.Context) ([]LoadedFleetBinding, error) {
		loads++
		return []LoadedFleetBinding{refreshTestBinding("a")}, nil
	}, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx, time.Millisecond, func(err error) {
			if err != nil {
				t.Error(err)
			}
			cancel()
		})
	}()
	select {
	case <-done:
		if loads < 2 {
			t.Fatal("refresh loop did not poll")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh loop did not stop")
	}
}

func TestDiscoveryFleetAcceptsEmptyAndRepopulates(t *testing.T) {
	loaded := []LoadedFleetBinding{refreshTestBinding("a")}
	f, err := NewDiscoveryFleet(t.Context(), "127.0.0.1:17801", func(context.Context) ([]LoadedFleetBinding, error) { return loaded, nil }, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded = []LoadedFleetBinding{}
	if err := f.Refresh(t.Context()); err != nil || fleetStatus(f, "a.example") != 421 {
		t.Fatal("last tenant removal failed")
	}
	loaded = []LoadedFleetBinding{refreshTestBinding("b")}
	if err := f.Refresh(t.Context()); err != nil || fleetStatus(f, "b.example") != 204 {
		t.Fatal("empty fleet could not repopulate")
	}
}

type fakeReadyWorker struct {
	mu       sync.Mutex
	alive    bool
	draining bool
	status   int
	closes   int
}

func (w *fakeReadyWorker) ServeHTTP(rw http.ResponseWriter, _ *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.alive || w.draining {
		http.Error(rw, "worker unavailable", http.StatusServiceUnavailable)
		return
	}
	rw.WriteHeader(w.status)
}

func (w *fakeReadyWorker) Alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.alive
}

func (w *fakeReadyWorker) BeginDrain() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.draining = true
}

func (w *fakeReadyWorker) Close(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.alive = false
	w.closes++
	return nil
}

func (w *fakeReadyWorker) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.alive = false
}

func (w *fakeReadyWorker) closeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closes
}

func TestDiscoveryReadyWorkersIsolateNewHostsAndRevokeThem(t *testing.T) {
	a, b, c, added := refreshTestBinding("a"), refreshTestBinding("b"), refreshTestBinding("c"), refreshTestBinding("added")
	loaded := []LoadedFleetBinding{a, b, c}
	var loadErr error
	builds := 0
	workerStarts := 0
	var mode string
	var latest *fakeReadyWorker
	factory := func(ctx context.Context, binding LoadedFleetBinding) (ReadyWorker, error) {
		workerStarts++
		if binding.Binding.BindingID != "added" {
			t.Fatalf("unexpected worker for existing tenant %q", binding.Binding.BindingID)
		}
		switch mode {
		case "error":
			return nil, errors.New("test isolated worker failure")
		case "timeout":
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			latest = &fakeReadyWorker{alive: true, status: http.StatusAccepted}
			return latest, nil
		}
	}
	f, err := NewDiscoveryFleetWithReadyWorkers(t.Context(), "127.0.0.1:17801", func(context.Context) ([]LoadedFleetBinding, error) {
		return loaded, loadErr
	}, func(Config) (http.Handler, error) {
		builds++
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
	}, factory, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 3 {
		t.Fatalf("initial incumbents built=%d want 3", builds)
	}
	assertIncumbents := func(t *testing.T) {
		t.Helper()
		for _, host := range []string{"a.example", "b.example", "c.example"} {
			if got := fleetStatus(f, host); got != http.StatusNoContent {
				t.Fatalf("incumbent %s status=%d", host, got)
			}
		}
	}
	assertIncumbents(t)

	// A complete snapshot may include an unready new Registry, but this must
	// not start a worker, rebuild, or drain any incumbent.
	added.RegistryUnavailable = true
	loaded = []LoadedFleetBinding{a, b, c, added}
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusServiceUnavailable || builds != 3 {
		t.Fatalf("unready addition status=%d builds=%d", got, builds)
	}

	// Unready -> ready is promoted only after a successful independent worker.
	added.RegistryUnavailable = false
	loaded = []LoadedFleetBinding{a, b, c, added}
	mode = "ready"
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusAccepted || builds != 3 || latest == nil {
		t.Fatalf("ready addition status=%d builds=%d worker=%v starts=%d", got, builds, latest != nil, workerStarts)
	}

	// A worker process exit cuts only that Host off, then the next successful
	// complete snapshot retries it without reconstructing incumbents.
	exited := latest
	exited.stop()
	if got := fleetStatus(f, "added.example"); got != http.StatusServiceUnavailable {
		t.Fatalf("exited worker status=%d", got)
	}
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusAccepted || exited.closeCount() == 0 || builds != 3 {
		t.Fatalf("worker recovery status=%d closes=%d builds=%d", got, exited.closeCount(), builds)
	}

	// Revocation (authoritative removal) must suppress the Host immediately
	// and terminate its child; the three unrelated tenants keep serving.
	revoked := latest
	loaded = []LoadedFleetBinding{a, b, c}
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusMisdirectedRequest || revoked.closeCount() == 0 || builds != 3 {
		t.Fatalf("revoked status=%d closes=%d builds=%d", got, revoked.closeCount(), builds)
	}

	// Process-start error and timeout are local 503s, never a reason to use an
	// old worker or drain existing authorization paths.
	mode = "error"
	loaded = []LoadedFleetBinding{a, b, c, added}
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusServiceUnavailable || builds != 3 {
		t.Fatalf("error addition status=%d builds=%d", got, builds)
	}
	mode = "timeout"
	if err := f.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIncumbents(t)
	if got := fleetStatus(f, "added.example"); got != http.StatusServiceUnavailable || builds != 3 {
		t.Fatalf("timeout addition status=%d builds=%d", got, builds)
	}

	// Conversely, a GNAS/discovery failure remains fleet-wide fail closed;
	// workers cannot turn a rejected authority snapshot into stale access.
	loadErr = errors.New("authoritative snapshot unavailable")
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("source failure accepted")
	}
	for _, host := range []string{"a.example", "b.example", "c.example", "added.example"} {
		if got := fleetStatus(f, host); got != http.StatusServiceUnavailable {
			t.Fatalf("authority failure host=%s status=%d", host, got)
		}
	}
}

type blockingReadyWorker struct {
	mu           sync.Mutex
	alive        bool
	accepting    bool
	inflight     int
	entered      chan struct{}
	release      chan struct{}
	drained      chan struct{}
	beginDrained chan struct{}
	beginGate    chan struct{}
	once         sync.Once
	drainOnce    sync.Once
	beginOnce    sync.Once
}

func newBlockingReadyWorker() *blockingReadyWorker {
	return &blockingReadyWorker{
		alive: true, accepting: true, entered: make(chan struct{}), release: make(chan struct{}),
		drained: make(chan struct{}), beginDrained: make(chan struct{}),
	}
}

func (w *blockingReadyWorker) ServeHTTP(response http.ResponseWriter, _ *http.Request) {
	w.mu.Lock()
	if !w.alive || !w.accepting {
		w.mu.Unlock()
		http.Error(response, "worker unavailable", http.StatusServiceUnavailable)
		return
	}
	w.inflight++
	w.mu.Unlock()
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	w.inflight--
	if !w.accepting && w.inflight == 0 {
		w.drainOnce.Do(func() { close(w.drained) })
	}
	w.mu.Unlock()
	response.WriteHeader(http.StatusNoContent)
}

func (w *blockingReadyWorker) Alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.alive
}

func (w *blockingReadyWorker) BeginDrain() {
	w.mu.Lock()
	w.accepting = false
	if w.inflight == 0 {
		w.drainOnce.Do(func() { close(w.drained) })
	}
	w.mu.Unlock()
	w.beginOnce.Do(func() { close(w.beginDrained) })
	if w.beginGate != nil {
		<-w.beginGate
	}
}

func (w *blockingReadyWorker) Close(ctx context.Context) error {
	w.BeginDrain()
	select {
	case <-w.drained:
		w.mu.Lock()
		w.alive = false
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		w.mu.Lock()
		w.alive = false
		w.mu.Unlock()
		return ctx.Err()
	}
}

func TestDiscoveryWorkerRevocationFencesOldRouterBeforeDrain(t *testing.T) {
	a, b, c, added := refreshTestBinding("a"), refreshTestBinding("b"), refreshTestBinding("c"), refreshTestBinding("added")
	added.RegistryUnavailable = true
	loaded := []LoadedFleetBinding{a, b, c, added}
	var worker *blockingReadyWorker
	f, err := NewDiscoveryFleetWithReadyWorkers(t.Context(), "127.0.0.1:17801", func(context.Context) ([]LoadedFleetBinding, error) {
		return loaded, nil
	}, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
	}, func(_ context.Context, binding LoadedFleetBinding) (ReadyWorker, error) {
		if binding.Binding.BindingID != "added" {
			t.Fatalf("unexpected isolated worker for %q", binding.Binding.BindingID)
		}
		worker = newBlockingReadyWorker()
		return worker, nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	added.RegistryUnavailable = false
	loaded = []LoadedFleetBinding{a, b, c, added}
	if err := f.Refresh(t.Context()); err != nil || worker == nil {
		t.Fatalf("worker promotion err=%v worker=%v", err, worker != nil)
	}
	inflight := make(chan int, 1)
	go func() { inflight <- fleetStatus(f, "added.example") }()
	select {
	case <-worker.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not receive an in-flight request")
	}
	// Revoke while the first request is still proxied.  BeginDrain deliberately
	// blocks publication here so the test observes the previous router snapshot:
	// the worker admission fence, not timing, must reject the new request.
	worker.beginGate = make(chan struct{})
	loaded = []LoadedFleetBinding{a, b, c}
	refreshed := make(chan error, 1)
	go func() { refreshed <- f.Refresh(t.Context()) }()
	select {
	case <-worker.beginDrained:
	case <-time.After(time.Second):
		t.Fatal("revocation did not fence the worker")
	}
	if status := fleetStatus(f, "added.example"); status != http.StatusServiceUnavailable {
		t.Fatalf("old router admitted a request after revocation fence: %d", status)
	}
	select {
	case err := <-refreshed:
		t.Fatalf("refresh completed before the old request drained: %v", err)
	default:
	}
	close(worker.beginGate)
	close(worker.release)
	if status := <-inflight; status != http.StatusNoContent {
		t.Fatalf("existing request status=%d", status)
	}
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	if status := fleetStatus(f, "added.example"); status != http.StatusMisdirectedRequest {
		t.Fatalf("revoked route status=%d", status)
	}
	for _, host := range []string{"a.example", "b.example", "c.example"} {
		if status := fleetStatus(f, host); status != http.StatusNoContent {
			t.Fatalf("incumbent %s status=%d", host, status)
		}
	}
}

func TestDiscoveryWorkerParentCloseFencesAndDrains(t *testing.T) {
	a, b, c, added := refreshTestBinding("a"), refreshTestBinding("b"), refreshTestBinding("c"), refreshTestBinding("added")
	added.RegistryUnavailable = true
	loaded := []LoadedFleetBinding{a, b, c, added}
	var worker *blockingReadyWorker
	f, err := NewDiscoveryFleetWithReadyWorkers(t.Context(), "127.0.0.1:17801", func(context.Context) ([]LoadedFleetBinding, error) {
		return loaded, nil
	}, func(Config) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
	}, func(_ context.Context, binding LoadedFleetBinding) (ReadyWorker, error) {
		if binding.Binding.BindingID != "added" {
			t.Fatalf("unexpected isolated worker for %q", binding.Binding.BindingID)
		}
		worker = newBlockingReadyWorker()
		return worker, nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	added.RegistryUnavailable = false
	loaded = []LoadedFleetBinding{a, b, c, added}
	if err := f.Refresh(t.Context()); err != nil || worker == nil {
		t.Fatalf("worker promotion err=%v worker=%v", err, worker != nil)
	}
	inflight := make(chan int, 1)
	go func() { inflight <- fleetStatus(f, "added.example") }()
	select {
	case <-worker.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not receive an in-flight request")
	}
	shutdownContext, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- f.Close(shutdownContext) }()
	select {
	case <-worker.beginDrained:
	case <-time.After(time.Second):
		t.Fatal("parent close did not fence the worker")
	}
	if status := fleetStatus(f, "added.example"); status != http.StatusServiceUnavailable {
		t.Fatalf("parent close admitted a new request: %d", status)
	}
	select {
	case err := <-closed:
		t.Fatalf("parent close returned before drain: %v", err)
	default:
	}
	close(worker.release)
	if status := <-inflight; status != http.StatusNoContent {
		t.Fatalf("existing request status=%d", status)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if worker.Alive() {
		t.Fatal("parent close left worker alive")
	}
	if err := f.Refresh(t.Context()); !errors.Is(err, ErrFleetClosed) {
		t.Fatalf("closed fleet refresh error=%v", err)
	}
}
