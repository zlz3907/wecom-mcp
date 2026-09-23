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

type fleetSnapshot struct {
	router      *HostRouter
	unavailable bool
}

// RefreshingFleet publishes a complete validated generation at once. An
// unsuccessful refresh disables serving until a complete refresh succeeds:
// stale database authority must not keep a removed tenant available.
type RefreshingFleet struct {
	load       FleetLoader
	build      FleetHandlerFactory
	mu         sync.Mutex
	execution  sync.RWMutex
	bindings   map[string]LoadedFleetBinding
	handlers   map[string]http.Handler
	listen     string
	allowEmpty bool
	current    atomic.Pointer[fleetSnapshot]
}

func NewRefreshingFleet(ctx context.Context, load FleetLoader, build FleetHandlerFactory) (*RefreshingFleet, error) {
	return newRefreshingFleet(ctx, "", load, build)
}

// NewDiscoveryFleet accepts an authoritative empty snapshot. The listener is
// supplied independently of tenants so deleting the last binding is valid.
func NewDiscoveryFleet(ctx context.Context, listen string, load FleetLoader, build FleetHandlerFactory) (*RefreshingFleet, error) {
	if err := validateListenAddress(listen); err != nil || !listenIsLoopback(listen) {
		return nil, fmt.Errorf("discovery listener must be loopback")
	}
	return newRefreshingFleet(ctx, listen, load, build)
}

func newRefreshingFleet(ctx context.Context, listen string, load FleetLoader, build FleetHandlerFactory) (*RefreshingFleet, error) {
	if load == nil || build == nil {
		return nil, fmt.Errorf("fleet loader and handler factory are required")
	}
	fleet := &RefreshingFleet{load: load, build: build, listen: listen, allowEmpty: listen != ""}
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
	for _, binding := range loaded {
		id := binding.Binding.BindingID
		if id == "" || handlers[id] != nil || binding.Config.ListenAddress != listen {
			return fmt.Errorf("fleet binding identity or listener is inconsistent")
		}
		// Reuse unchanged services, including their concurrency limits and
		// identity state. Polling must not create a new service every interval.
		handler := f.handlers[id]
		if handler == nil || !reflect.DeepEqual(f.bindings[id], binding) {
			handler, err = f.build(binding.Config)
			if err != nil {
				return fmt.Errorf("fleet binding handler initialization failed")
			}
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
	if previous := f.current.Load(); previous != nil && !reflect.DeepEqual(f.bindings, bindings) {
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
	f.bindings, f.handlers = bindings, handlers
	f.current.Store(&fleetSnapshot{router: router})
	return nil
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
