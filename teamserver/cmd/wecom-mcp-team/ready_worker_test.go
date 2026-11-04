package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
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
