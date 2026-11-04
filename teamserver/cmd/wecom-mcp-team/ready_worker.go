package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/teamserver/internal/team"
)

const bindingWorkerMessageLimit = 64 << 10

// startReadyWorker runs the same executable in a private child mode. The
// child receives no credential over stdin or argv: OAuth credentials are read
// from its inherited protected environment and its one ready response is
// authenticated before a reverse proxy route can be published.
func startReadyWorker(ctx context.Context, binding team.LoadedFleetBinding, _ *slog.Logger) (team.ReadyWorker, error) {
	start, err := team.NewBindingWorkerStart(binding)
	if err != nil {
		return nil, fmt.Errorf("invalid ready worker binding")
	}
	payload, err := json.Marshal(start)
	if err != nil || len(payload) > bindingWorkerMessageLimit {
		return nil, fmt.Errorf("ready worker start payload is invalid")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("ready worker handshake unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("ready worker executable unavailable")
	}
	command := exec.Command(executable, "--gnas-ready-worker")
	command.Env = append(os.Environ(), "TEAM_MCP_WORKER_READY_TOKEN="+hex.EncodeToString(secret))
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ready worker pipe unavailable")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ready worker pipe unavailable")
	}
	// Never copy a child stderr to parent logs: a future dependency must not be
	// able to leak credentials, assertions, or Enterprise WeCom identities.
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("ready worker start failed")
	}
	if _, err := stdin.Write(payload); err != nil {
		_ = stdin.Close()
		killAndWait(command)
		return nil, fmt.Errorf("ready worker start payload failed")
	}
	if err := stdin.Close(); err != nil {
		killAndWait(command)
		return nil, fmt.Errorf("ready worker start payload failed")
	}
	type readyResult struct {
		ready team.BindingWorkerReady
		err   error
	}
	readyResultChannel := make(chan readyResult, 1)
	go func() {
		var ready team.BindingWorkerReady
		decoder := json.NewDecoder(io.LimitReader(stdout, bindingWorkerMessageLimit+1))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&ready)
		readyResultChannel <- readyResult{ready: ready, err: err}
	}()
	select {
	case result := <-readyResultChannel:
		if result.err != nil || team.VerifyBindingWorkerReady(secret, start.Identity, result.ready) != nil {
			killAndWait(command)
			return nil, fmt.Errorf("ready worker handshake rejected")
		}
		return newSubprocessReadyWorker(command, result.ready.Address), nil
	case <-ctx.Done():
		killAndWait(command)
		return nil, ctx.Err()
	}
}

type subprocessReadyWorker struct {
	proxy     *httputil.ReverseProxy
	command   *exec.Cmd
	done      chan struct{}
	alive     atomic.Bool
	closeOnce sync.Once
}

func newSubprocessReadyWorker(command *exec.Cmd, address string) *subprocessReadyWorker {
	target := &url.URL{Scheme: "http", Host: address}
	worker := &subprocessReadyWorker{command: command, done: make(chan struct{})}
	worker.alive.Store(true)
	worker.proxy = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			// The child sees only loopback. Preserve the exact host that already
			// passed the parent HostRouter rather than trusting a client header.
			request.Out.Host = request.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "MCP instance unavailable", http.StatusServiceUnavailable)
		},
	}
	go func() {
		_ = command.Wait()
		worker.alive.Store(false)
		close(worker.done)
	}()
	return worker
}

func (w *subprocessReadyWorker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	w.proxy.ServeHTTP(response, request)
}

func (w *subprocessReadyWorker) Alive() bool { return w.alive.Load() }

func (w *subprocessReadyWorker) Close(ctx context.Context) error {
	var closeErr error
	w.closeOnce.Do(func() {
		w.alive.Store(false)
		if w.command.Process != nil {
			if err := w.command.Process.Signal(syscall.SIGTERM); err != nil {
				closeErr = err
			}
		}
	})
	select {
	case <-w.done:
		return closeErr
	case <-ctx.Done():
		if w.command.Process != nil {
			_ = w.command.Process.Kill()
		}
		select {
		case <-w.done:
		case <-time.After(time.Second):
		}
		return ctx.Err()
	}
}

func killAndWait(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

func runReadyWorker(ctx context.Context, logger *slog.Logger) error {
	secret, err := hex.DecodeString(os.Getenv("TEAM_MCP_WORKER_READY_TOKEN"))
	if err != nil || len(secret) != 32 {
		return fmt.Errorf("ready worker handshake unavailable")
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, bindingWorkerMessageLimit+1))
	decoder.DisallowUnknownFields()
	var start team.BindingWorkerStart
	if err := decoder.Decode(&start); err != nil || decoder.Decode(&struct{}{}) != io.EOF || start.Validate() != nil {
		return fmt.Errorf("ready worker start request is invalid")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("ready worker listener unavailable")
	}
	defer listener.Close()
	cfg, err := start.LoadConfig(listener.Addr().String())
	if err != nil {
		return err
	}
	handler, err := bindingHandler(cfg, logger.With("binding_id", cfg.AuthorizationTenant))
	if err != nil {
		return fmt.Errorf("ready worker handler unavailable")
	}
	ready := team.BindingWorkerReady{Version: team.BindingWorkerProtocolVersion, BindingDigest: start.Identity.BindingDigest, Tenant: start.Identity.Tenant, Resource: start.Identity.Resource, Address: listener.Addr().String()}
	ready.MAC, err = team.SignBindingWorkerReady(secret, ready)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
		return fmt.Errorf("ready worker response failed")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	select {
	case err := <-serveResult:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("ready worker stopped")
		}
		return nil
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		return nil
	}
}
