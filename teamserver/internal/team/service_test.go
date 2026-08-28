package team

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuthMetadataAndUnauthorizedChallenge(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	response := postRPC(t, server.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if challenge := response.Header.Get("WWW-Authenticate"); !strings.Contains(challenge, "resource_metadata=") {
		t.Fatalf("challenge=%q", challenge)
	}

	metadataResponse, err := http.Get(server.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer metadataResponse.Body.Close()
	var metadata map[string]any
	if err := json.NewDecoder(metadataResponse.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["resource"] != "https://mcp.example.test/mcp" {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestTenantPrefixedOAuthMetadataAndChallenge(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.PublicURL = "https://mcp.jyiai.com/gmzoop"
	cfg.MCPURL = cfg.PublicURL + "/mcp"
	cfg.MetadataURL = "https://mcp.jyiai.com/.well-known/oauth-protected-resource/gmzoop/mcp"
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, _ string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return nil, sdkauth.ErrInvalidToken
	}
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()

	response := postRPC(t, server.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if challenge := response.Header.Get("WWW-Authenticate"); !strings.Contains(challenge, `resource_metadata="`+cfg.MetadataURL+`"`) {
		t.Fatalf("challenge=%q", challenge)
	}

	metadataResponse, err := http.Get(server.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer metadataResponse.Body.Close()
	var metadata map[string]any
	if err := json.NewDecoder(metadataResponse.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["resource"] != cfg.MCPURL {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestReaderAndAdminReceiveDifferentToolLists(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()

	reader := listTools(t, server.URL, "reader")
	if !reader["wecom_record_query"] || reader["wecom_record_apply"] || reader["wecom_registry_bootstrap"] {
		t.Fatalf("reader tools=%#v", reader)
	}
	admin := listTools(t, server.URL, "admin")
	if !admin["wecom_record_query"] || !admin["wecom_record_apply"] || !admin["wecom_registry_bootstrap"] {
		t.Fatalf("admin tools=%#v", admin)
	}
}

func TestAuthenticatedWorkBuddyStyleInitialize(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()
	response := postRPC(t, server.URL+"/mcp", "reader", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"workbuddy-test","version":"1"}}}`)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"name":"guomai-aite-wecom-team-mcp"`)) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}

func TestLoopbackProxyPreservesSDKDNSRebindingProtection(t *testing.T) {
	upstream := newTestHTTPServer(t)
	defer upstream.Close()

	directRequest, err := http.NewRequest(http.MethodPost, upstream.URL+"/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	directRequest.Host = "mcp.example.test"
	directRequest.Header.Set("Authorization", "Bearer reader")
	directRequest.Header.Set("Content-Type", "application/json")
	directRequest.Header.Set("Accept", "application/json, text/event-stream")
	directRequest.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	directResponse, err := http.DefaultClient.Do(directRequest)
	if err != nil {
		t.Fatal(err)
	}
	directResponse.Body.Close()
	if directResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("external Host sent directly to loopback upstream status=%d", directResponse.StatusCode)
	}

	target, _ := url.Parse(upstream.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = target.Host
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()
	tools := listTools(t, proxyServer.URL, "reader")
	if !tools["wecom_record_query"] {
		t.Fatalf("trusted loopback proxy could not reach MCP: %#v", tools)
	}
}

func TestReaderCannotForgeWriteToolCall(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()
	response := postRPC(t, server.URL+"/mcp", "reader", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"wecom_record_apply","arguments":{}}}`)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"error"`)) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if bytes.Contains(body, []byte(`"structuredContent"`)) {
		t.Fatalf("forged write call reached a tool handler: %s", body)
	}
}

func TestAuthenticatedButUnauthorizedRequestsReturnForbidden(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.RequiredScopes = []string{"wecom.mcp"}
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		info := &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour), UserID: "test-user",
			Extra: map[string]any{"issuer": "https://login.example.test"},
		}
		if token == "missing-scope" {
			info.Extra["role"] = string(RoleReader)
		} else if token == "missing-role" {
			info.Scopes = []string{"wecom.mcp"}
		}
		return info, nil
	}
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()

	missingScope := postRPC(t, server.URL+"/mcp", "missing-scope", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	missingScope.Body.Close()
	if missingScope.StatusCode != http.StatusForbidden {
		t.Fatalf("missing scope status=%d", missingScope.StatusCode)
	}
	challenge := missingScope.Header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, `scope="wecom.mcp"`) {
		t.Fatalf("missing scope challenge=%q", challenge)
	}

	missingRole := postRPC(t, server.URL+"/mcp", "missing-role", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	missingRole.Body.Close()
	if missingRole.StatusCode != http.StatusForbidden {
		t.Fatalf("missing role status=%d", missingRole.StatusCode)
	}
}

func TestToolConcurrencyLimitFailsClosed(t *testing.T) {
	cfg := testServiceConfig(t)
	cfg.MaxConcurrentTools = 1
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	service.toolSlots <- struct{}{}
	defer func() { <-service.toolSlots }()
	var definitionFound bool
	for _, definition := range service.definitions {
		if definition.Name != "wecom_schema_status" {
			continue
		}
		definitionFound = true
		result, callErr := service.callTool(context.Background(), nil, RoleReader, definition)
		if callErr != nil || result == nil || !result.IsError {
			t.Fatalf("callErr=%v result=%#v", callErr, result)
		}
		textContent, ok := result.Content[0].(*sdkmcp.TextContent)
		if !ok || !strings.Contains(textContent.Text, "concurrency limit") {
			t.Fatalf("result=%#v", result)
		}
	}
	if !definitionFound {
		t.Fatal("wecom_schema_status definition missing")
	}
}

func TestHealthEndpointsDoNotExposeInstanceDetails(t *testing.T) {
	server := newTestHTTPServer(t)
	defer server.Close()
	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), "tenant") || strings.Contains(string(body), "instance") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	readyResponse, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer readyResponse.Body.Close()
	if readyResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("missing instance config readiness status=%d", readyResponse.StatusCode)
	}
}

func TestReadOnlyToolCallReusesBusinessLayerAndWritesRedactedAudit(t *testing.T) {
	directory := t.TempDir()
	schemaPath := filepath.Join(directory, "schema.md")
	schema := "# test\n"
	for index := 1; index <= 9; index++ {
		role := fmt.Sprintf("Z-S%02d", index)
		schema += fmt.Sprintf("## %s｜test\n| 字段 | Field ID | 类型 | 属性 |\n| --- | --- | --- | --- |\n| 标题 | `field%d` | `FIELD_TYPE_TEXT` | — |\n", role, index)
	}
	if err := os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "instance.json")
	instance := map[string]any{
		"version": 1, "instance_name": "test-instance", "tenant_route": "test-route",
		"registry_document_id": "registry1", "registry_key": "registry-key",
		"schema_mirror_path": schemaPath, "state_path": filepath.Join(directory, "state.json"),
		"api_whitelist": map[string][]string{"read": {"get_records"}},
	}
	encoded, _ := json.Marshal(instance)
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNAS_BASE_URL", "https://gnas.example.test")
	t.Setenv("GNAS_APP_ID", "test-app")
	t.Setenv("GNAS_APP_SECRET", "SECRET-CANARY")

	var audit bytes.Buffer
	cfg := Config{
		InstanceConfigPath: configPath,
		PublicURL:          "https://mcp.example.test", MCPURL: "https://mcp.example.test/mcp",
		MetadataURL: "https://mcp.example.test/.well-known/oauth-protected-resource",
		OIDCIssuer:  "https://login.example.test", AdvertisedScopes: []string{"openid"},
		AuditHMACKey: []byte("0123456789abcdef0123456789abcdef"), MaxConcurrentTools: 16,
	}
	service, err := NewService(cfg, slog.New(slog.NewJSONHandler(&audit, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, _ string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour), UserID: "test-user",
			Extra: map[string]any{"role": string(RoleReader), "issuer": "https://login.example.test"},
		}, nil
	}
	server := httptest.NewServer(service.Handler(verifier))
	defer server.Close()
	readyResponse, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	readyResponse.Body.Close()
	if readyResponse.StatusCode != http.StatusOK {
		t.Fatalf("valid instance config readiness status=%d", readyResponse.StatusCode)
	}
	response := postRPC(t, server.URL+"/mcp", "opaque-test-token", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wecom_schema_status","arguments":{}}}`)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"schema_is_local_read_only_mirror":true`)) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	auditText := audit.String()
	if !strings.Contains(auditText, `"tool":"wecom_schema_status"`) || !strings.Contains(auditText, `"subject_hash":"`) {
		t.Fatalf("audit=%s", auditText)
	}
	for _, forbidden := range []string{"test-user", "opaque-test-token", "SECRET-CANARY", "schema_is_local_read_only_mirror"} {
		if strings.Contains(auditText, forbidden) {
			t.Fatalf("audit leaked %q: %s", forbidden, auditText)
		}
	}
}

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := testServiceConfig(t)
	service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	verifier := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		role := Role(token)
		if roleRank(role) == 0 {
			return nil, sdkauth.ErrInvalidToken
		}
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour),
			UserID:     "test-user",
			Extra:      map[string]any{"role": string(role), "issuer": "https://login.example.test"},
		}, nil
	}
	return httptest.NewServer(service.Handler(verifier))
}

func testServiceConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		InstanceConfigPath: t.TempDir() + "/instance.json",
		PublicURL:          "https://mcp.example.test",
		MCPURL:             "https://mcp.example.test/mcp",
		MetadataURL:        "https://mcp.example.test/.well-known/oauth-protected-resource",
		OIDCIssuer:         "https://login.example.test",
		RequiredScopes:     nil,
		AdvertisedScopes:   []string{"openid", "profile"},
		AuditHMACKey:       []byte("0123456789abcdef0123456789abcdef"),
		MaxConcurrentTools: 16,
	}
}

func listTools(t *testing.T, baseURL, token string) map[string]bool {
	t.Helper()
	response := postRPC(t, baseURL+"/mcp", token, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]bool, len(envelope.Result.Tools))
	for _, tool := range envelope.Result.Tools {
		result[tool.Name] = true
	}
	return result
}

func postRPC(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
