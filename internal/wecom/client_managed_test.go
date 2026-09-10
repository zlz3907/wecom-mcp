package wecom

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagedExecutorTransportPreservesFixedOperation(t *testing.T) {
	for _, operation := range []string{"get_sheet", "get_records", "add_records", "update_records", "list_employees"} {
		t.Run(operation, func(t *testing.T) {
			definition := Operations[operation]
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/gnas/service/wecomExecute" || r.Method != http.MethodPost || r.Header.Get("X-GNAS-Upstream-Method") != definition.Method || r.Header.Get("X-GNAS-Upstream-Path") != definition.Path || r.Header.Get("X-GNAS-Managed-Source") != "fixed-instance" || r.Header.Get("X-Auth-Type") != "service_jwt" {
					t.Error("request escaped the fixed managed transport")
				}
				body, _ := io.ReadAll(r.Body)
				if definition.Method == http.MethodGet {
					if len(body) != 0 {
						t.Error("upstream GET carried body")
					}
				} else if string(body) != `{"docid":"fixture"}` {
					t.Errorf("body changed: %s", body)
				}
				calls++
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0})
			}))
			defer server.Close()
			c := &Client{baseURL: server.URL, route: "fixed-instance", managedExecutor: true, token: "fixture-token", expiresAt: time.Now().Add(time.Hour).Unix(), httpClient: server.Client()}
			if _, err := c.Request(context.Background(), operation, map[string]string{"docid": "fixture"}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if _, err := c.Request(context.Background(), "unlisted", nil); err == nil || calls != 1 {
				t.Fatal("unlisted operation reached transport")
			}
		})
	}
}

func TestManagedExecutorTransportIsExplicit(t *testing.T) {
	t.Setenv("GNAS_BASE_URL", "https://example.invalid")
	t.Setenv("GNAS_APP_ID", "fixture-app")
	t.Setenv("GNAS_APP_SECRET", "fixture-secret")
	for _, mode := range []string{"", "legacy_proxy", "managed_executor", "invalid"} {
		t.Setenv("GNAS_WECOM_TRANSPORT", mode)
		c, err := NewFromEnvironment("fixed-instance")
		if mode == "invalid" {
			if err == nil {
				t.Fatal("invalid transport accepted")
			}
			continue
		}
		if err != nil || c.managedExecutor != (mode == "managed_executor") {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}

func TestManagedExecutorRejectsGatewayErrorsWithoutUpstreamErrcode(t *testing.T) {
	for _, status := range []int{400, 403, 500, 503} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":50301,"message":"PRIVATE-CANARY"}`))
		}))
		c := &Client{baseURL: server.URL, route: "fixed-instance", managedExecutor: true, token: "fixture-token", expiresAt: time.Now().Add(time.Hour).Unix(), httpClient: server.Client()}
		result, err := c.Request(context.Background(), "update_records", map[string]any{})
		server.Close()
		if err == nil || result != nil || calls != 1 || strings.Contains(err.Error(), "PRIVATE-CANARY") {
			t.Fatalf("gateway error treated as success or leaked: status=%d calls=%d", status, calls)
		}
	}
}

func TestManagedExecutorRejectsNonJSONAndExhaustedAuthRetry(t *testing.T) {
	for _, status := range []int{401, 503} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/gnas/service/getJwtToken" {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"token": "refreshed-fixture", "expires_at": time.Now().Add(time.Hour).Unix()}})
				return
			}
			calls++
			w.WriteHeader(status)
			_, _ = w.Write([]byte("PRIVATE-NON-JSON-CANARY"))
		}))
		c := &Client{baseURL: server.URL, route: "fixed-instance", managedExecutor: true, token: "fixture-token", expiresAt: time.Now().Add(time.Hour).Unix(), httpClient: server.Client()}
		result, err := c.Request(context.Background(), "get_records", map[string]any{})
		server.Close()
		wantCalls := 1
		if status == 401 {
			wantCalls = 2
		}
		if err == nil || result != nil || calls != wantCalls || strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("gateway retry/failure contract: status=%d calls=%d", status, calls)
		}
	}
}
