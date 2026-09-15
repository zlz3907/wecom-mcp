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

type tableOAuthTransport func(*http.Request) (*http.Response, error)

func (f tableOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Real SDK, OAuth introspection verification, trusted employee entry, table
// handlers and managed request construction; fake authorization/GNAS upstreams.
// This does not test QR login, token issuance, a real gateway or WeCom writes.
func TestOAuth21ManagedTableWriteAndQueryEndToEnd(t *testing.T) {
	for _, scenario := range []string{"success", "missing personnel", "forged identity", "gateway 503"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := testServiceConfig(t)
			dir := filepath.Dir(cfg.InstanceConfigPath)
			schema := ""
			for i := 1; i <= 9; i++ {
				schema += fmt.Sprintf("## Z-S0%d｜表\n| 测试 | field | FIELD_TYPE_TEXT |\n", i)
				if i == 6 {
					schema += "| 自动规划授权 | checkbox | FIELD_TYPE_CHECKBOX |\n"
					schema += "| 发起主体 | initiator | FIELD_TYPE_REFERENCE |\n| 执行主体 | executor | FIELD_TYPE_REFERENCE |\n"
				}
				if i == 9 {
					schema += "| 企业微信成员或责任人 | member | FIELD_TYPE_USER |\n| 主体类型 | type | FIELD_TYPE_SINGLE_SELECT |\n| 主体状态 | status | FIELD_TYPE_SINGLE_SELECT |\n"
				}
			}
			schemaPath := filepath.Join(dir, "schema.md")
			if err := os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			runtime := config.Config{Version: 1, InstanceName: "fixture-instance", TenantRoute: "fixture-source", RegistryDocumentID: "registry", RegistryKey: "fixture-key", SchemaMirrorPath: schemaPath, SchemaSource: "local_compatibility", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "application-operator", AIExecutionSubjectRecordID: "ai-subject", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}, "zoop_records_write": {"list_employees", "add_records"}}}
			encoded, _ := json.Marshal(runtime)
			if err := os.WriteFile(cfg.InstanceConfigPath, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GNAS_BASE_URL", "https://fake.invalid")
			t.Setenv("GNAS_APP_ID", "fake-app")
			t.Setenv("GNAS_APP_SECRET", "fake-secret")
			t.Setenv("GNAS_WECOM_TRANSPORT", "managed_executor")
			cfg.AuthenticationMode, cfg.UserAuthorizationEnabled = AuthenticationModeOAuth21, true
			cfg.OIDCAudience = cfg.MCPURL
			cfg.OAuth21IntrospectionURL = "https://fake.invalid/introspect"
			cfg.OAuth21ClientID, cfg.OAuth21ClientSecret = "fake-resource", "0123456789abcdef0123456789abcdef"
			cfg.AuthorizationTenant, cfg.AuthorizationResource = "fixture-tenant", "fixture-resource"
			cfg.RequiredScopes = []string{"zoop.read"}
			writes, readbacks, introspections := 0, 0, 0
			var stored []any
			var resourceHost string
			previous := http.DefaultTransport
			http.DefaultTransport = tableOAuthTransport(func(r *http.Request) (response *http.Response, transportErr error) {
				defer func() {
					if transportErr != nil {
						t.Errorf("fake transport rejected request: %v", transportErr)
					}
				}()
				// Only our exact local MCP server can use the real transport.
				if resourceHost != "" && r.URL.Host == resourceHost {
					return previous.RoundTrip(r)
				}
				if r.URL.Host != "fake.invalid" {
					return nil, fmt.Errorf("unexpected network destination")
				}
				status := http.StatusOK
				var out any
				switch r.URL.Path {
				case "/introspect":
					id, secret, ok := r.BasicAuth()
					if !ok || id != cfg.OAuth21ClientID || secret != cfg.OAuth21ClientSecret || r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("token") != "fake-access" || r.Form.Get("resource") != cfg.MCPURL {
						return nil, fmt.Errorf("invalid introspection request")
					}
					introspections++
					out = map[string]any{"active": true, "iss": cfg.OIDCIssuer, "sub": "wecom:fixture:employee-one", "aud": cfg.MCPURL, "exp": time.Now().Add(time.Minute).Unix(), "scope": "zoop.read zoop.write", "token_use": "access", "tenant": cfg.AuthorizationTenant, "wecom_userid": "employee-one", "mcp_role": "member", "effective_tools": []string{"wecom_record_apply", "wecom_record_query"}}
				case "/gnas/service/getJwtToken":
					out = map[string]any{"code": 200, "data": map[string]any{"token": "fake-jwt", "expires_at": time.Now().Add(time.Hour).Unix()}}
				case "/gnas/service/wecomExecute":
					if r.Method != "POST" || r.Header.Get("X-GNAS-Managed-Source") != "fixture-source" {
						return nil, fmt.Errorf("wrong managed route")
					}
					op := ""
					for _, name := range []string{"get_sheet", "get_fields", "get_records", "list_employees", "add_records"} {
						if r.Header.Get("X-GNAS-Upstream-Path") == wecom.Operations[name].Path {
							op = name
						}
					}
					if op == "" || r.Header.Get("X-GNAS-Upstream-Method") != wecom.Operations[op].Method {
						return nil, fmt.Errorf("unexpected managed operation")
					}
					if op == "list_employees" {
						out = map[string]any{"userlist": []any{map[string]any{"userid": "application-operator", "status": 1}}}
						break
					}
					var p map[string]any
					if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
						return nil, err
					}
					switch {
					case op == "get_sheet" && p["docid"] == "registry":
						out = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "registry-sheet"}}}
					case op == "get_fields" && p["docid"] == "registry":
						fields := []any{}
						for _, k := range []string{"registry_key", "docid", "lifecycle_status"} {
							fields = append(fields, map[string]any{"field_title": k, "field_id": k})
						}
						out = map[string]any{"fields": fields}
					case op == "get_records" && p["docid"] == "registry":
						values := map[string]any{}
						for k, v := range map[string]string{"registry_key": "fixture-key", "docid": "instance-doc", "lifecycle_status": "active"} {
							values[k] = []any{map[string]any{"text": v}}
						}
						out = map[string]any{"records": []any{map[string]any{"values": values}}}
					case op == "get_sheet" && p["docid"] == "instance-doc":
						out = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "personnel", "title": "Z-S09｜主体"}, map[string]any{"type": "smartsheet", "sheet_id": "sessions", "title": "Z-S06｜会话"}}}
					case op == "get_records" && p["docid"] == "instance-doc" && p["sheet_id"] == "personnel":
						rows := []any{}
						if scenario != "missing personnel" {
							rows = append(rows, map[string]any{"record_id": "human-subject", "values": map[string]any{"member": []any{map[string]any{"user_id": "employee-one"}}, "type": []any{map[string]any{"text": "人员主体"}}, "status": []any{map[string]any{"text": "启用"}}}})
						}
						out = map[string]any{"records": rows, "has_more": false}
					case op == "add_records" && p["docid"] == "instance-doc" && p["sheet_id"] == "sessions":
						if p["key_type"] != "CELL_VALUE_KEY_TYPE_FIELD_ID" {
							return nil, fmt.Errorf("wrong table field encoding")
						}
						writes++
						if scenario == "gateway 503" {
							status = 503
							out = map[string]any{"code": 50301, "message": "secret-canary"}
							break
						}
						records := p["records"].([]any)
						if len(records) != 1 {
							return nil, fmt.Errorf("unexpected write count")
						}
						values := records[0].(map[string]any)["values"].(map[string]any)
						if !reflect.DeepEqual(values["field"], []any{map[string]any{"type": "text", "text": "fixture-value"}}) {
							return nil, fmt.Errorf("unexpected business value: %v", values["field"])
						}
						if checked, ok := values["checkbox"].(bool); !ok || checked {
							return nil, fmt.Errorf("checkbox false changed type or value: %T", values["checkbox"])
						}
						for field, actor := range map[string]string{"initiator": "human-subject", "executor": "ai-subject"} {
							if !reflect.DeepEqual(values[field], []any{actor}) {
								return nil, fmt.Errorf("wrong actor field %s: %v", field, values[field])
							}
						}
						stored = []any{map[string]any{"record_id": "fixture-row", "values": values}}
						out = map[string]any{"errcode": 0, "records": stored}
					case op == "get_records" && p["docid"] == "instance-doc" && p["sheet_id"] == "sessions":
						readbacks++
						if readbacks == 2 && !reflect.DeepEqual(p["record_ids"], []any{"fixture-row"}) {
							return nil, fmt.Errorf("query did not preserve exact record ID")
						}
						out = map[string]any{"errcode": 0, "records": stored, "has_more": false}
					default:
						return nil, fmt.Errorf("unexpected or cross-instance table request")
					}
				default:
					return nil, fmt.Errorf("unexpected endpoint; legacy proxy forbidden")
				}
				body, _ := json.Marshal(out)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			auth, err := NewOAuth21IntrospectionAuthenticator(cfg)
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(service.Handler(auth.Verify))
			defer server.Close()
			resourceHost = strings.TrimPrefix(server.URL, "http://")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture-table-client", Version: "1"}, nil)
			session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRoundTripper{token: "fake-access", base: http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			args := map[string]any{"target_role": "Z-S06", "operation": "add_records", "idempotency_key": "fixture-table-write-key", "source_revision": "fixture-v1", "records": []any{map[string]any{"values": map[string]any{"测试": "fixture-value", "自动规划授权": false}}}}
			if scenario == "forged identity" {
				args["userid"] = "employee-two"
			}
			result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_record_apply", Arguments: args})
			if scenario != "success" {
				if err == nil && !result.IsError {
					t.Fatal("failed write reported successful")
				}
				want := 0
				if scenario == "gateway 503" {
					want = 1
				}
				if writes != want || len(stored) != 0 || readbacks != 0 {
					t.Fatalf("writes=%d stored=%d readbacks=%d", writes, len(stored), readbacks)
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), "secret-canary") {
					t.Fatal("gateway body leaked")
				}
				return
			}
			if err != nil || result.IsError {
				body, _ := json.Marshal(result)
				t.Fatalf("write: result=%s err=%v", body, err)
			}
			body, _ := json.Marshal(result.StructuredContent)
			var output map[string]any
			if err := json.Unmarshal(body, &output); err != nil {
				t.Fatal(err)
			}
			if output["state"] != "applied" || output["readback_verified"] != true || output["business_operator_userid"] != "employee-one" || output["verified_initiator_subject_record_id"] != "human-subject" || output["verified_execution_subject_record_id"] != "ai-subject" {
				t.Fatalf("write output=%s", body)
			}
			query, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "wecom_record_query", Arguments: map[string]any{"target_role": "Z-S06", "record_ids": []string{"fixture-row"}, "compact": false}})
			if err != nil || query.IsError {
				t.Fatalf("query=%+v err=%v", query, err)
			}
			body, _ = json.Marshal(query.StructuredContent)
			if err := json.Unmarshal(body, &output); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(output["records"], stored) || output["returned_count"] != float64(1) || writes != 1 || readbacks != 2 || introspections < 3 {
				t.Fatalf("query=%s writes=%d readbacks=%d introspections=%d", body, writes, readbacks, introspections)
			}
		})
	}
}
