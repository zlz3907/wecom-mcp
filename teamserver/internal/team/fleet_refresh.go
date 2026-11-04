package team

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type FleetLoader func(context.Context) ([]LoadedFleetBinding, error)
type FleetHandlerFactory func(Config) (http.Handler, error)

// ReadyWorker is an independently supervised tenant handler. Its factory must
// honour the supplied context by terminating any child it started before
// returning. A worker is used only for an additive/recovery transition after
// a complete authoritative discovery snapshot has been accepted.
type ReadyWorker interface {
	http.Handler
	Alive() bool
	// BeginDrain makes the worker reject every request that has not already
	// entered its proxy.  Refresh calls this before it publishes a revoked or
	// replaced route, so a request holding an older router snapshot cannot
	// bypass an authoritative removal.
	BeginDrain()
	Close(context.Context) error
}

type ReadyWorkerFactory func(context.Context, LoadedFleetBinding) (ReadyWorker, error)

type fleetSnapshot struct {
	router      *HostRouter
	unavailable bool
}

// RefreshingFleet publishes a complete validated generation at once. An
// unsuccessful refresh disables serving until a complete refresh succeeds:
// stale database authority must not keep a removed tenant available.
type RefreshingFleet struct {
	load      FleetLoader
	build     FleetHandlerFactory
	mu        sync.Mutex
	execution sync.RWMutex
	bindings  map[string]LoadedFleetBinding
	handlers  map[string]http.Handler
	workers   map[string]ReadyWorker
	// workerFailures is deliberately not an authorization cache. It records
	// only that a ready Host needs another local process-start attempt; every
	// request still reaches the live service and its introspection path.
	workerFailures map[string]bool
	workerFactory  ReadyWorkerFactory
	workerTimeout  time.Duration
	listen         string
	allowEmpty     bool
	current        atomic.Pointer[fleetSnapshot]
}

func NewRefreshingFleet(ctx context.Context, load FleetLoader, build FleetHandlerFactory) (*RefreshingFleet, error) {
	return newRefreshingFleet(ctx, "", load, build, nil, 0)
}

// NewDiscoveryFleet accepts an authoritative empty snapshot. The listener is
// supplied independently of tenants so deleting the last binding is valid.
func NewDiscoveryFleet(ctx context.Context, listen string, load FleetLoader, build FleetHandlerFactory) (*RefreshingFleet, error) {
	if err := validateListenAddress(listen); err != nil || !listenIsLoopback(listen) {
		return nil, fmt.Errorf("discovery listener must be loopback")
	}
	return newRefreshingFleet(ctx, listen, load, build, nil, 0)
}

// NewDiscoveryFleetWithReadyWorkers is an opt-in additive-host isolation
// mode. Existing constructors intentionally do not enable it: an operator
// must opt in explicitly, and authoritative discovery/source failures remain
// fleet-wide fail-closed exactly as before.
func NewDiscoveryFleetWithReadyWorkers(ctx context.Context, listen string, load FleetLoader, build FleetHandlerFactory, workers ReadyWorkerFactory, workerTimeout time.Duration) (*RefreshingFleet, error) {
	if err := validateListenAddress(listen); err != nil || !listenIsLoopback(listen) {
		return nil, fmt.Errorf("discovery listener must be loopback")
	}
	if workers == nil || workerTimeout <= 0 || workerTimeout > 30*time.Second {
		return nil, fmt.Errorf("ready worker factory and timeout from 1ms to 30s are required")
	}
	return newRefreshingFleet(ctx, listen, load, build, workers, workerTimeout)
}

func newRefreshingFleet(ctx context.Context, listen string, load FleetLoader, build FleetHandlerFactory, workers ReadyWorkerFactory, workerTimeout time.Duration) (*RefreshingFleet, error) {
	if load == nil || build == nil {
		return nil, fmt.Errorf("fleet loader and handler factory are required")
	}
	fleet := &RefreshingFleet{load: load, build: build, workers: map[string]ReadyWorker{}, workerFailures: map[string]bool{}, workerFactory: workers, workerTimeout: workerTimeout, listen: listen, allowEmpty: listen != ""}
	if err := fleet.Refresh(ctx); err != nil {
		return nil, err
	}
	return fleet, nil
}

// ListenAddress is fixed by the first generation; later changes are rejected.
func (f *RefreshingFleet) ListenAddress() string { return f.listen }

func (f *RefreshingFleet) Refresh(ctx context.Context) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if err != nil {
			if previous := f.current.Load(); previous != nil {
				f.current.Store(&fleetSnapshot{router: previous.router, unavailable: true})
			}
		}
	}()
	loaded, err := f.load(ctx)
	if err != nil {
		return err
	}
	if len(loaded) == 0 && !f.allowEmpty {
		return fmt.Errorf("fleet has no bindings")
	}
	listen := f.listen
	if len(loaded) > 0 {
		listen = loaded[0].Config.ListenAddress
	}
	if f.listen != "" && listen != f.listen {
		return fmt.Errorf("fleet refresh cannot change listen address")
	}
	bindings := make(map[string]LoadedFleetBinding, len(loaded))
	handlers := make(map[string]http.Handler, len(loaded))
	workers := make(map[string]ReadyWorker, len(loaded))
	workerFailures := make(map[string]bool, len(loaded))
	isolate := f.localWorkerTransition(loaded)
	started := make([]ReadyWorker, 0)
	defer func() {
		if err != nil {
			for _, worker := range started {
				f.closeWorker(worker)
			}
		}
	}()
	for _, binding := range loaded {
		id := binding.Binding.BindingID
		if id == "" || handlers[id] != nil || binding.Config.ListenAddress != listen {
			return fmt.Errorf("fleet binding identity or listener is inconsistent")
		}
		// Reuse unchanged services, including their concurrency limits and
		// identity state. Polling must not create a new service every interval.
		handler := f.handlers[id]
		old, existed := f.bindings[id]
		oldWorker := f.workers[id]
		if binding.RegistryUnavailable {
			// An authoritative but unready Host has no MCP/authentication handler,
			// including metadata and health paths. Never reuse its prior service.
			handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				http.Error(w, "MCP instance unavailable", http.StatusServiceUnavailable)
			})
			delete(workerFailures, id)
		} else if isolate && f.needsLocalWorker(id, old, existed, binding, oldWorker) {
			workerContext, cancel := context.WithTimeout(ctx, f.workerTimeout)
			worker, workerErr := f.workerFactory(workerContext, binding)
			workerContextErr := workerContext.Err()
			cancel()
			if workerErr != nil || workerContextErr != nil || worker == nil || !worker.Alive() {
				if worker != nil {
					f.closeWorker(worker)
				}
				workerFailures[id] = true
				handler = unavailableTenantHandler()
			} else {
				started = append(started, worker)
				workers[id] = worker
				handler = readyWorkerHandler(worker)
			}
		} else if oldWorker != nil && oldWorker.Alive() && reflect.DeepEqual(old, binding) {
			workers[id] = oldWorker
		} else if handler == nil || !reflect.DeepEqual(old, binding) || oldWorker != nil && !oldWorker.Alive() {
			handler, err = f.build(binding.Config)
			if err != nil {
				return fmt.Errorf("fleet binding handler initialization failed")
			}
		}
		if f.workerFailures[id] && !binding.RegistryUnavailable && workers[id] == nil {
			workerFailures[id] = true
		}
		if handler == nil {
			return fmt.Errorf("fleet binding handler is missing")
		}
		bindings[id], handlers[id] = binding, handler
	}
	router, err := NewHostRouter(loaded, handlers)
	if len(loaded) == 0 && f.allowEmpty {
		router, err = &HostRouter{handlers: map[string]http.Handler{}}, nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if previous := f.current.Load(); previous != nil && !reflect.DeepEqual(f.bindings, bindings) && !isolate {
		// Legacy state locks belong to each Service. Do not allow two
		// generations to execute against the same state file concurrently,
		// including remove-then-readd. Stop admissions before draining.
		f.current.Store(&fleetSnapshot{router: previous.router, unavailable: true})
		if err := f.drain(ctx); err != nil {
			return err
		}
		defer f.execution.Unlock()
	}
	if f.listen == "" {
		f.listen = listen
	}
	retired := f.retiredWorkers(workers)
	// First fence the old worker's own admission path.  A request may have
	// loaded the previous HostRouter immediately before the atomic publication
	// below; this per-worker fence makes that request fail closed unless it was
	// already in flight.  Do this before publishing the new router and before
	// waiting for its bounded drain.
	for _, worker := range retired {
		worker.BeginDrain()
	}
	f.bindings, f.handlers = bindings, handlers
	f.workers, f.workerFailures = workers, workerFailures
	f.current.Store(&fleetSnapshot{router: router})
	for _, worker := range retired {
		f.closeWorker(worker)
	}
	return nil
}

func unavailableTenantHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "MCP instance unavailable", http.StatusServiceUnavailable)
	})
}

func readyWorkerHandler(worker ReadyWorker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if worker == nil || !worker.Alive() {
			unavailableTenantHandler().ServeHTTP(w, r)
			return
		}
		worker.ServeHTTP(w, r)
	})
}

// localWorkerTransition permits only host-local changes after a healthy
// complete generation. Any source failure, ordinary binding mutation, or
// listener drift stays on the established fleet-wide fail-closed path.
func (f *RefreshingFleet) localWorkerTransition(loaded []LoadedFleetBinding) bool {
	previous := f.current.Load()
	if f.workerFactory == nil || previous == nil || previous.unavailable || len(f.bindings) == 0 {
		return false
	}
	next := make(map[string]LoadedFleetBinding, len(loaded))
	changed := false
	for _, binding := range loaded {
		id := binding.Binding.BindingID
		if _, duplicate := next[id]; duplicate {
			return false
		}
		next[id] = binding
		old, exists := f.bindings[id]
		if !exists {
			changed = true
			continue
		}
		if reflect.DeepEqual(old, binding) {
			if worker := f.workers[id]; worker != nil && !worker.Alive() {
				changed = true
			}
			if f.workerFailures[id] {
				changed = true
			}
			continue
		}
		if !sameBindingExceptRegistryReadiness(old, binding) {
			return false
		}
		changed = true
	}
	for id := range f.bindings {
		if _, found := next[id]; !found {
			changed = true
		}
	}
	return changed
}

func sameBindingExceptRegistryReadiness(left, right LoadedFleetBinding) bool {
	left.RegistryUnavailable = false
	right.RegistryUnavailable = false
	return reflect.DeepEqual(left, right)
}

func (f *RefreshingFleet) needsLocalWorker(id string, old LoadedFleetBinding, existed bool, binding LoadedFleetBinding, oldWorker ReadyWorker) bool {
	if !existed || old.RegistryUnavailable || f.workerFailures[id] {
		return true
	}
	return oldWorker != nil && (!oldWorker.Alive() || !reflect.DeepEqual(old, binding))
}

func (f *RefreshingFleet) retiredWorkers(next map[string]ReadyWorker) []ReadyWorker {
	retired := make([]ReadyWorker, 0)
	for id, old := range f.workers {
		if old != nil && next[id] != old {
			retired = append(retired, old)
		}
	}
	return retired
}

func (f *RefreshingFleet) closeWorker(worker ReadyWorker) {
	if worker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.workerTimeout)
	defer cancel()
	_ = worker.Close(ctx)
}

// drain returns with the execution write lock held. Waiting is bounded even
// for callers outside Run; a timeout leaves the old generation unavailable.
func (f *RefreshingFleet) drain(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.execution.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Run is synchronous and stops on cancellation. The caller owns its goroutine.
// Reports contain no configuration or upstream response body.
func (f *RefreshingFleet) Run(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := f.Refresh(refreshContext)
			cancel()
			if report != nil && ctx.Err() == nil {
				report(err)
			}
		}
	}
}

func (f *RefreshingFleet) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	f.execution.RLock()
	defer f.execution.RUnlock()
	snapshot := f.current.Load()
	if snapshot == nil {
		http.Error(w, "MCP fleet unavailable", http.StatusServiceUnavailable)
		return
	}
	if snapshot.unavailable && snapshot.router.handlerForHost(request.Host) != nil {
		http.Error(w, "MCP fleet unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot.router.ServeHTTP(w, request)
}
