package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	instanceconfig "github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/teamserver/internal/team"
)

// This is the child executable used by TestReadyWorkerSubprocessProxyAndExit.
// It runs only under an explicit test environment marker and never reaches a
// GNAS endpoint or an Enterprise WeCom API.
func TestReadyWorkerProcessHelper(t *testing.T) {
	if os.Getenv("TEST_READY_WORKER_PROCESS") != "1" {
		return
	}
	if os.Getenv("TEST_READY_WORKER_HOLD") == "1" {
		select {}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runReadyWorker(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Error("worker helper failed")
	}
}

// TestReadyWorkerDrainProcessHelper is an intentionally test-only child.  It
// exercises the parent process boundary with a long-running proxied request
// and records only received signal names, never its ready secret or config.
func TestReadyWorkerDrainProcessHelper(t *testing.T) {
	if os.Getenv("TEST_READY_WORKER_DRAIN_PROCESS") != "1" {
		return
	}
	secret, err := hex.DecodeString(os.Getenv("TEAM_MCP_WORKER_READY_TOKEN"))
	if err != nil || len(secret) != 32 {
		t.Fatal("missing test handshake secret")
	}
	var start team.BindingWorkerStart
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, bindingWorkerMessageLimit+1))
	if err := decoder.Decode(&start); err != nil || decoder.Decode(&struct{}{}) != io.EOF || start.Validate() != nil {
		t.Fatal("invalid test worker start")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := team.BindingWorkerReady{Version: team.BindingWorkerProtocolVersion, BindingDigest: start.Identity.BindingDigest, Tenant: start.Identity.Tenant, Resource: start.Identity.Resource, Address: listener.Addr().String()}
	ready.MAC, err = team.SignBindingWorkerReady(secret, ready)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
		t.Fatal(err)
	}
	trace := os.Getenv("TEST_READY_WORKER_DRAIN_TRACE")
	writeTrace := func(event string) {
		if trace == "" {
			return
		}
		f, openErr := os.OpenFile(trace, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if openErr == nil {
			_, _ = f.WriteString(event + "\n")
			_ = f.Close()
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/block":
			enteredOnce.Do(func() { close(entered); writeTrace("entered") })
			<-release
			w.WriteHeader(http.StatusNoContent)
		case "/release":
			releaseOnce.Do(func() { close(release) })
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case signalValue := <-signals:
		if signalValue == syscall.SIGTERM {
			writeTrace("TERM")
			_ = server.Close()
			return
		}
		writeTrace("INT")
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	case err := <-serveResult:
		if err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}
}

func readyWorkerFixture(t *testing.T) team.LoadedFleetBinding {
	t.Helper()
	t.Setenv("TEAM_MCP_AUTH_MODE", "oauth21")
	t.Setenv("TEAM_MCP_AUDIT_HMAC_KEY", strings.Repeat("a", 32))
	t.Setenv("TEAM_MCP_OAUTH21_INTROSPECTION_URL", "http://127.0.0.1:1/gnas/oauth/introspect")
	t.Setenv("GNAS_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("GNAS_APP_ID", "test-app")
	t.Setenv("GNAS_APP_SECRET", "test-app-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	runtime := instanceconfig.Config{
		Version: 1, InstanceName: "worker", TenantRoute: "source-worker", RegistryDocumentID: "doc-worker", RegistryKey: "registry-worker",
		SchemaSource: "z-s00", StatePath: filepath.Join(t.TempDir(), "state.json"), APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}},
	}
	cfg, err := team.LoadConfigForBinding("", "127.0.0.1:17801", team.BindingOverrides{
		Runtime: &runtime, OAuth21ServiceJWT: true, GNASBindingDigest: strings.Repeat("a", 64),
		PublicURL: "https://worker.example", OIDCIssuer: "https://worker.example/gnas/oauth", AuthorizationTenant: "worker", AuthorizationResource: "zoop", Plugins: []string{"zoop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.TrustedLoopbackProxy = true
	return team.LoadedFleetBinding{Binding: team.FleetBinding{BindingID: "worker", Hosts: []string{"worker.example"}, PublicURL: "https://worker.example", AuthorizationTenant: "worker", AuthorizationResource: "zoop"}, Config: cfg}
}

func TestReadyWorkerSubprocessProxyAndExit(t *testing.T) {
	binding := readyWorkerFixture(t)
	t.Setenv("TEST_READY_WORKER_PROCESS", "1")
	original := readyWorkerCommand
	readyWorkerCommand = func(string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestReadyWorkerProcessHelper$", "--")
	}
	defer func() { readyWorkerCommand = original }()
	worker, err := startReadyWorker(t.Context(), binding, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	process, ok := worker.(*subprocessReadyWorker)
	if !ok || !worker.Alive() {
		t.Fatal("authenticated subprocess worker was not promoted")
	}
	checkStatus := func(handler http.Handler, want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "http://worker.example/healthz", nil)
		request.Host = "worker.example"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("proxy status=%d want=%d", response.Code, want)
		}
	}
	checkStatus(worker, http.StatusOK)
	if err := process.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for worker.Alive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if worker.Alive() {
		t.Fatal("worker exit was not observed")
	}
	checkStatus(worker, http.StatusServiceUnavailable)
	if err := worker.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A fresh authenticated child is independently promotable after an exit;
	// closing it models the route's revocation/kill lifecycle.
	recovered, err := startReadyWorker(t.Context(), binding, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	checkStatus(recovered, http.StatusOK)
	if err := recovered.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkStatus(recovered, http.StatusServiceUnavailable)
}

func TestReadyWorkerStartupTimeoutKillsChild(t *testing.T) {
	binding := readyWorkerFixture(t)
	t.Setenv("TEST_READY_WORKER_PROCESS", "1")
	t.Setenv("TEST_READY_WORKER_HOLD", "1")
	original := readyWorkerCommand
	readyWorkerCommand = func(string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestReadyWorkerProcessHelper$", "--")
	}
	defer func() { readyWorkerCommand = original }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	worker, err := startReadyWorker(ctx, binding, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worker != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout worker=%v err=%v", worker != nil, err)
	}
}

func TestReadyWorkerDrainRejectsNewRequestsBeforeGracefulStop(t *testing.T) {
	binding := readyWorkerFixture(t)
	trace := filepath.Join(t.TempDir(), "signals.log")
	t.Setenv("TEST_READY_WORKER_DRAIN_PROCESS", "1")
	t.Setenv("TEST_READY_WORKER_DRAIN_TRACE", trace)
	original := readyWorkerCommand
	readyWorkerCommand = func(string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestReadyWorkerDrainProcessHelper$", "--")
	}
	defer func() { readyWorkerCommand = original }()
	worker, err := startReadyWorker(t.Context(), binding, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	process := worker.(*subprocessReadyWorker)
	blocked := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "http://worker.example/block", nil)
		req.Host = "worker.example"
		response := httptest.NewRecorder()
		worker.ServeHTTP(response, req)
		blocked <- response.Code
	}()
	waitForWorkerTrace(t, trace, "entered")
	closeContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- worker.Close(closeContext) }()
	assertWorkerStatusEventually(t, worker, http.StatusServiceUnavailable)
	select {
	case status := <-blocked:
		t.Fatalf("in-flight request ended before release: %d", status)
	default:
	}
	if !worker.Alive() {
		t.Fatal("draining worker was terminated before its request completed")
	}
	release, err := http.Get("http://" + process.address + "/release")
	if err != nil {
		t.Fatal(err)
	}
	_ = release.Body.Close()
	if release.StatusCode != http.StatusNoContent {
		t.Fatalf("release status=%d", release.StatusCode)
	}
	if status := <-blocked; status != http.StatusNoContent {
		t.Fatalf("in-flight request status=%d", status)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	waitForWorkerTrace(t, trace, "INT")
	if bytes, err := os.ReadFile(trace); err != nil || strings.Contains(string(bytes), "TERM") {
		t.Fatalf("graceful drain sent SIGTERM: err=%v trace=%q", err, bytes)
	}
}

func TestReadyWorkerDrainTimeoutUsesSIGTERM(t *testing.T) {
	binding := readyWorkerFixture(t)
	trace := filepath.Join(t.TempDir(), "signals.log")
	t.Setenv("TEST_READY_WORKER_DRAIN_PROCESS", "1")
	t.Setenv("TEST_READY_WORKER_DRAIN_TRACE", trace)
	original := readyWorkerCommand
	readyWorkerCommand = func(string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestReadyWorkerDrainProcessHelper$", "--")
	}
	defer func() { readyWorkerCommand = original }()
	worker, err := startReadyWorker(t.Context(), binding, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "http://worker.example/block", nil)
		req.Host = "worker.example"
		response := httptest.NewRecorder()
		worker.ServeHTTP(response, req)
		blocked <- response.Code
	}()
	waitForWorkerTrace(t, trace, "entered")
	closeContext, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := worker.Close(closeContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	waitForWorkerTrace(t, trace, "TERM")
	if bytes, err := os.ReadFile(trace); err != nil || strings.Contains(string(bytes), "INT") {
		t.Fatalf("timed-out drain was not SIGTERM-only: err=%v trace=%q", err, bytes)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("timed-out proxied request did not end")
	}
}

func waitForWorkerTrace(t *testing.T, path, event string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bytes, err := os.ReadFile(path); err == nil && strings.Contains(string(bytes), event+"\n") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	bytes, _ := os.ReadFile(path)
	t.Fatalf("worker trace missing %q: %q", event, bytes)
}

func assertWorkerStatusEventually(t *testing.T, handler http.Handler, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "http://worker.example/healthz", nil)
		req.Host = "worker.example"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("worker did not reach status=%d", want)
}
