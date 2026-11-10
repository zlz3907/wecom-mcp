package team

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

// TestGNASCrossServiceGroupAndToolFlow exercises the real SDK, resource server,
// GNAS OAuth center and group policy resolver in two isolated processes. The
// member directory and WeCom API are synthetic; no production data is used.
func TestGNASCrossServiceGroupAndToolFlow(t *testing.T) {
	file := os.Getenv("MCP_CROSS_FIXTURE_FILE")
	if file == "" {
		t.Skip("cross-service fixture not requested")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		URL           string `json:"url"`
		Token         string `json:"token"`
		BindingDigest string `json:"binding_digest"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || !strings.HasPrefix(fixture.URL, "http://127.0.0.1:") || fixture.Token == "" || fixture.BindingDigest == "" {
		t.Fatal("invalid synthetic fixture")
	}
	control := func(phase string) {
		req, err := http.NewRequest(http.MethodPost, fixture.URL+"/fixture-control?phase="+phase, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("phase %s status %d", phase, resp.StatusCode)
		}
	}
	cfg := testServiceConfig(t)
	dir := filepath.Dir(cfg.InstanceConfigPath)
	schemaPath := filepath.Join(dir, "schema.md")
	schema := ""
	for i := 1; i <= 9; i++ {
		schema += fmt.Sprintf("## Z-S0%d｜表\n| 测试 | field | FIELD_TYPE_TEXT |\n", i)
		if i == 9 {
			schema += "| 企业微信成员或责任人 | member | FIELD_TYPE_USER |\n| 主体类型 | type | FIELD_TYPE_SINGLE_SELECT |\n| 主体状态 | status | FIELD_TYPE_SINGLE_SELECT |\n"
		}
	}
	if err = os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Config{Version: 1, InstanceName: "cross-fixture", TenantRoute: "source-a", RegistryDocumentID: "registry", RegistryKey: "fixture-key", SchemaMirrorPath: schemaPath, SchemaSource: "local_compatibility", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "application-operator", AIExecutionSubjectRecordID: "ai-subject", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}, "directory": {"list_employees"}}}
	encoded, _ := json.Marshal(runtime)
	if err = os.WriteFile(cfg.InstanceConfigPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNAS_BASE_URL", "https://fake.invalid")
	t.Setenv("GNAS_APP_ID", "fake-app")
	t.Setenv("GNAS_APP_SECRET", "fake-secret")
	t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
	const resource = "https://mcp.jyiai.com/gmzoop/mcp"
	cfg.AuthenticationMode = AuthenticationModeOAuth21
	cfg.UserAuthorizationEnabled = true
	cfg.PublicURL = "https://mcp.jyiai.com/gmzoop"
	cfg.MCPURL = resource
	cfg.MetadataURL = "https://mcp.jyiai.com/.well-known/oauth-protected-resource/gmzoop/mcp"
	cfg.OIDCIssuer = "https://jyiai.com/gnas/oauth"
	cfg.OIDCAudience = resource
	cfg.OAuth21ServiceJWT = true
	cfg.OAuth21IntrospectionURL = fixture.URL + "/gnas/service/introspectMCPTokenV1"
	cfg.AuthorizationTokenEndpoint = fixture.URL + "/gnas/service/getJwtToken"
	cfg.AuthorizationServiceAppID = "app-a"
	cfg.AuthorizationServiceAppSecret = "cross-service-secret"
	cfg.GNASBindingDigest = fixture.BindingDigest
	cfg.Runtime = &runtime
	cfg.AuthorizationTenant = "tenant-a"
	cfg.AuthorizationResource = "gmzoop"
	cfg.RequiredScopes = []string{"zoop.read"}
	previous := http.DefaultTransport
	var serverHost string
	http.DefaultTransport = tableOAuthTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == strings.TrimPrefix(fixture.URL, "http://") || (serverHost != "" && r.URL.Host == serverHost) {
			return previous.RoundTrip(r)
		}
		if r.URL.Host != "fake.invalid" {
			return nil, fmt.Errorf("unexpected synthetic host")
		}
		var result any
		switch r.URL.Path {
		case "/gnas/service/getJwtToken":
			result = map[string]any{"code": 200, "data": map[string]any{"token": "fake-jwt", "expires_at": time.Now().Add(time.Hour).Unix()}}
		case "/gnas/service/wecomExecute":
			op := ""
			for _, name := range []string{"get_sheet", "get_fields", "get_records", "list_employees"} {
				if r.Header.Get("X-GNAS-Upstream-Path") == wecom.Operations[name].Path {
					op = name
				}
			}
			if op == "list_employees" {
				result = map[string]any{"userlist": []any{map[string]any{"userid": "application-operator", "status": 1}, map[string]any{"userid": "member-001", "status": 1}}}
				break
			}
			var p map[string]any
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				return nil, err
			}
			switch {
			case op == "get_sheet" && p["docid"] == "registry":
				result = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "registry-sheet"}}}
			case op == "get_fields" && p["docid"] == "registry":
				fields := []any{}
				for _, k := range []string{"registry_key", "docid", "lifecycle_status"} {
					fields = append(fields, map[string]any{"field_title": k, "field_id": k})
				}
				result = map[string]any{"fields": fields}
			case op == "get_records" && p["docid"] == "registry":
				values := map[string]any{}
				for k, v := range map[string]string{"registry_key": "fixture-key", "docid": "instance-doc", "lifecycle_status": "active"} {
					values[k] = []any{map[string]any{"text": v}}
				}
				result = map[string]any{"records": []any{map[string]any{"values": values}}}
			case op == "get_sheet" && p["docid"] == "instance-doc":
				result = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "personnel", "title": "Z-S09｜主体"}}}
			case op == "get_records" && p["docid"] == "instance-doc" && p["sheet_id"] == "personnel":
				result = map[string]any{"errcode": 0, "records": []any{}, "has_more": false}
			default:
				return nil, fmt.Errorf("unexpected synthetic operation %s", op)
			}
		default:
			return nil, fmt.Errorf("unexpected synthetic endpoint")
		}
		body, _ := json.Marshal(result)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	auth, err := NewOAuth21IntrospectionAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if svc.config.PolicyFloorPath != "" {
		t.Fatal("cross-service flow unexpectedly requires a local policy file")
	}
	server := httptest.NewServer(svc.Handler(auth.Verify))
	defer server.Close()
	serverHost = strings.TrimPrefix(server.URL, "http://")
	// Enable the binding-scoped catalog client after Handler creation so this
	// fixture drives the poll/report deterministically rather than by timer.
	svc.config.CapabilityCatalogPushEnabled = true
	svc.config.CapabilityCatalogEndpoint = fixture.URL + "/gnas/service/reportMCPCapabilityCatalogV1"
	svc.config.CapabilityCatalogPollEndpoint = fixture.URL + "/gnas/service/pollMCPCapabilityRefreshV1"
	svc.config.CapabilityCatalogBindingID = "tenant-a"
	svc.config.CapabilityCatalogSource = "source-a"
	svc.config.CapabilityCatalogPublicResource = cfg.PublicURL
	svc.config.CapabilityCatalogAuthorizationResource = cfg.AuthorizationResource
	if id, err := svc.pollCapabilityRefresh(context.Background()); err != nil || id != "" {
		t.Fatalf("unselected binding unexpectedly requested: %q %v", id, err)
	}
	request, err := http.NewRequest(http.MethodPost, fixture.URL+"/fixture-catalog-request", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var wanted struct {
		RequestID string `json:"refresh_request_id"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&wanted) != nil {
		response.Body.Close()
		t.Fatal("cannot request synthetic catalog")
	}
	response.Body.Close()
	refreshID, err := svc.pollCapabilityRefresh(context.Background())
	if err != nil || refreshID == "" || refreshID != wanted.RequestID {
		t.Fatalf("poll mismatch: %q %v", refreshID, err)
	}
	if err = svc.pushCapabilityCatalog(context.Background(), refreshID); err != nil {
		t.Fatalf("report rejected: %v", err)
	}
	if id, err := svc.pollCapabilityRefresh(context.Background()); err != nil || id != "" {
		t.Fatalf("completed request still pending: %q %v", id, err)
	}
	response, err = http.DefaultClient.Get(fixture.URL + "/fixture-catalog-status")
	if err != nil {
		t.Fatal(err)
	}
	var completed struct {
		ID      string `json:"completed_id"`
		Reports int    `json:"reports"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&completed) != nil {
		response.Body.Close()
		t.Fatal("cannot read synthetic catalog status")
	}
	response.Body.Close()
	if completed.ID != refreshID || completed.Reports != 1 {
		t.Fatalf("wrong catalog completion: %+v", completed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "cross-flow-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRoundTripper{token: fixture.Token, base: http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	names := func() map[string]bool {
		got, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, tool := range got.Tools {
			out[tool.Name] = true
		}
		return out
	}
	allTools := func() map[string]bool {
		all := make(map[string]bool)
		for _, definition := range svc.definitions {
			if allows(RolePolicy, definition.Access) {
				all[definition.Name] = true
			}
		}
		return all
	}
	assertOpenCatalog := func(phase string) {
		t.Helper()
		got := names()
		// These established capabilities are independent of the implementation's
		// definitions and ensure an empty or accidentally reduced catalog fails.
		for _, name := range []string{"wecom_schema_status", "wecom_employee_list", "wecom_record_query", "wecom_record_apply"} {
			if !got[name] {
				t.Fatalf("%s catalog missing required tool %s", phase, name)
			}
		}
		if !reflect.DeepEqual(got, allTools()) {
			t.Fatalf("%s policy did not expose full authorized catalog: %v", phase, got)
		}
	}
	assertOpenCatalog("open")
	control("group")
	if got := names(); len(got) != 1 || !got["wecom_schema_status"] {
		t.Fatalf("group policy tools: %v", got)
	}
	allowed, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_schema_status", Arguments: map[string]any{}})
	if err != nil || allowed == nil || allowed.IsError {
		t.Fatalf("allowed call: result=%+v err=%v", allowed, err)
	}
	denied, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_employee_list", Arguments: map[string]any{}})
	if err != nil || denied == nil || !denied.IsError {
		t.Fatalf("ungranted tool should return a normal tool error: result=%+v err=%v", denied, err)
	}
	unknown, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "unknown_synthetic_tool", Arguments: map[string]any{}})
	if err != nil || unknown == nil || !unknown.IsError {
		t.Fatalf("unknown tool should return the same normal error: result=%+v err=%v", unknown, err)
	}
	if !reflect.DeepEqual(denied.Content, unknown.Content) {
		t.Fatal("unavailable tool error revealed whether a tool exists")
	}
	if got := names(); len(got) != 1 || !got["wecom_schema_status"] {
		t.Fatalf("denied call disrupted the session: %v", got)
	}
	control("override")
	if got := names(); len(got) != 1 || !got["wecom_employee_list"] {
		t.Fatalf("individual override: %v", got)
	}
	allowed, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_employee_list", Arguments: map[string]any{}})
	if err != nil || allowed == nil || allowed.IsError {
		t.Fatalf("individual override call: result=%+v err=%v", allowed, err)
	}
	control("empty")
	if got := names(); len(got) != 0 {
		t.Fatalf("empty override: %v", got)
	}
	denied, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_employee_list", Arguments: map[string]any{}})
	if err != nil || denied == nil || !denied.IsError {
		t.Fatalf("revoked call should return a normal tool error: result=%+v err=%v", denied, err)
	}
	control("disabled")
	assertOpenCatalog("disabled")
	control("revoked")
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("disabled member retained access")
	}
}
