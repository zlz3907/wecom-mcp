package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

type oauthPersonnelFake struct {
	records []any
	calls   int
}

type oauthFakeTransport func(*http.Request) (*http.Response, error)

type personnelPagingFake struct {
	records []any
	fail    bool
}

type personnelInvalidFake struct{ response map[string]any }

func (f personnelInvalidFake) Request(context.Context, string, any) (map[string]any, error) {
	return f.response, nil
}

func TestPersonnelStatusRejectsMalformedPagesAndRows(t *testing.T) {
	paused := identitySubjectRecord("id", "employee-one", "人员主体", "暂停")
	missingMember := identitySubjectRecord("id", "employee-one", "人员主体", "暂停")
	delete(missingMember["values"].(map[string]any), "member")
	for name, response := range map[string]map[string]any{
		"missing result":     {},
		"missing errcode":    {"result": map[string]any{"records": []any{}, "has_more": false}},
		"null records":       {"result": map[string]any{"errcode": float64(0), "records": nil, "has_more": false}},
		"missing pagination": {"result": map[string]any{"errcode": float64(0), "records": []any{paused}}},
		"missing subject":    {"result": map[string]any{"errcode": float64(0), "records": []any{missingMember}, "has_more": false}},
		"filter ignored":     {"result": map[string]any{"errcode": float64(0), "records": []any{identitySubjectRecord("other-id", "other-user", "人员主体", "启用")}, "has_more": false}},
		"incomplete page":    {"result": map[string]any{"errcode": float64(0), "records": []any{paused}, "has_more": true}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readPersonnelSubjectRecords(context.Background(), personnelInvalidFake{response}, "doc", "sheet", []string{"member", "type", "status"}, "employee-one"); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}

func (f personnelPagingFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	if f.fail || operation != "get_records" {
		return nil, fmt.Errorf("unavailable")
	}
	input := payload.(map[string]any)
	filtered := filterFakePersonnel(f.records, input)
	start := input["offset"].(int)
	end := start + input["limit"].(int)
	if start > len(filtered) {
		return nil, fmt.Errorf("bad offset")
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	return map[string]any{"result": map[string]any{"errcode": float64(0), "records": filtered[start:end], "has_more": end < len(filtered)}}, nil
}

func filterFakePersonnel(records []any, input map[string]any) []any {
	filter := input["filter_spec"].(map[string]any)
	condition := filter["conditions"].([]any)[0].(map[string]any)
	value := condition["user_value"].(map[string]any)["value"]
	var userid string
	switch list := value.(type) {
	case []string:
		userid = list[0]
	case []any:
		userid, _ = list[0].(string)
	}
	matched := []any{}
	for _, item := range records {
		row, _ := item.(map[string]any)
		values, _ := row["values"].(map[string]any)
		if identityCellContainsUserID(values["member"], userid) {
			matched = append(matched, item)
		}
	}
	return matched
}

func TestPersonnelStatusFiltersLargeTableAndFailsClosed(t *testing.T) {
	records := make([]any, 0, 201)
	for i := 0; i < 200; i++ {
		records = append(records, identitySubjectRecord(fmt.Sprintf("other-%d", i), fmt.Sprintf("other-%d", i), "人员主体", "启用"))
	}
	records = append(records, identitySubjectRecord("last", "employee-one", "人员主体", "暂停"))
	all, err := readPersonnelSubjectRecords(context.Background(), personnelPagingFake{records: records}, "doc", "sheet", []string{"member", "type", "status"}, "employee-one")
	if err != nil || len(all) != 1 {
		t.Fatalf("filtered lookup failed: %d %v", len(all), err)
	}
	if _, err := resolveUniquePersonnelSubjectRecordID(all, "employee-one", "member", "type", "status"); err == nil {
		t.Fatal("paused person in large table was accepted")
	}
	if _, err := readPersonnelSubjectRecords(context.Background(), personnelPagingFake{fail: true}, "doc", "sheet", []string{"member", "type", "status"}, "employee-one"); err == nil {
		t.Fatal("unavailable personnel status failed open")
	}
	duplicates := make([]any, 200)
	for i := range duplicates {
		duplicates[i] = identitySubjectRecord(fmt.Sprintf("duplicate-%d", i), "employee-one", "人员主体", "启用")
	}
	if _, err := readPersonnelSubjectRecords(context.Background(), personnelPagingFake{records: duplicates}, "doc", "sheet", []string{"member", "type", "status"}, "employee-one"); err == nil {
		t.Fatal("full page of matching records was accepted as complete")
	}
}

func (f oauthFakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the public trusted entrypoint and the real message write handler,
// including fixed-route HTTP construction, idempotency and actor audit. Every
// HTTP request is intercepted; this test has no network fallback.
func TestOAuthEmployeeWriteEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, employee string
		forged         bool
		wantWrites     int
	}{
		{"verified employee", "employee-one", false, 1},
		{"missing personnel", "employee-two", false, 1},
		{"missing employee", "", false, 0},
		{"forged binding", "employee-one", true, 0},
		{"configuration changes during lookup", "employee-one", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			schemaPath := filepath.Join(dir, "schema.md")
			schema := ""
			for i := 1; i <= 9; i++ {
				schema += fmt.Sprintf("## Z-S0%d｜表\n| 测试 | field | FIELD_TYPE_TEXT |\n", i)
			}
			schema += "| 企业微信成员或责任人 | member | FIELD_TYPE_USER |\n| 主体类型 | type | FIELD_TYPE_SINGLE_SELECT |\n| 主体状态 | status | FIELD_TYPE_SINGLE_SELECT |\n"
			if err := os.WriteFile(schemaPath, []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			runtime := config.Config{Version: 1, InstanceName: "fixture-instance", TenantRoute: "fixture-source", RegistryDocumentID: "registry", RegistryKey: "instance-key", SchemaMirrorPath: schemaPath, SchemaSource: "local_compatibility", StatePath: filepath.Join(dir, "state.json"), WecomOperatorUserID: "application-operator", AIExecutionSubjectRecordID: "ai-subject", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}, "default": {"list_employees", "send_app_message"}}}
			configPath := filepath.Join(dir, "instance.json")
			encoded, _ := json.Marshal(runtime)
			if err := os.WriteFile(configPath, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GNAS_BASE_URL", "https://fake.invalid")
			t.Setenv("GNAS_APP_ID", "fake-app")
			t.Setenv("GNAS_APP_SECRET", "fake-secret")
			fake := &oauthPersonnelFake{records: []any{identitySubjectRecord("human-subject", "employee-one", "人员主体", "启用"), identitySubjectRecord("ai-subject", "employee-one", "AI 执行主体", "启用")}}
			writes := 0
			previous := http.DefaultTransport
			http.DefaultTransport = oauthFakeTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "fake.invalid" {
					return nil, fmt.Errorf("unexpected host")
				}
				var response any
				if r.URL.Path == "/gnas/service/getJwtToken" {
					response = map[string]any{"code": 200, "data": map[string]any{"token": "fake-jwt", "expires_at": time.Now().Add(time.Hour).Unix()}}
				} else if r.URL.Path == "/gnas/service/wecomExecute" {
					if r.Header.Get("X-GNAS-Managed-Source") != "fixture-source" || r.Header.Get("X-GNAS-Upstream-Path") != "/cgi-bin/message/send" {
						return nil, fmt.Errorf("cross-instance write")
					}
					var message map[string]any
					if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
						return nil, err
					}
					if message["touser"] != "recipient-one" {
						return nil, fmt.Errorf("unexpected recipient")
					}
					writes++
					response = map[string]any{"errcode": 0, "msgid": "fake-message"}
				} else if r.URL.Path == "/api/fixture-source/cgi-bin/user/list" {
					response = map[string]any{"userlist": []any{map[string]any{"userid": "application-operator", "status": 1}, map[string]any{"userid": "employee-one", "status": 1}, map[string]any{"userid": "employee-two", "status": 1}, map[string]any{"userid": "recipient-one", "status": 1}}}
				} else {
					operation := ""
					for _, op := range []string{"get_sheet", "get_fields", "get_records"} {
						if r.URL.Path == "/api/fixture-source"+wecom.Operations[op].Path {
							operation = op
						}
					}
					if operation == "" {
						return nil, fmt.Errorf("unexpected or cross-instance read")
					}
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						return nil, err
					}
					result, err := fake.Request(r.Context(), operation, payload)
					if err != nil {
						return nil, err
					}
					if tc.name == "configuration changes during lookup" && operation == "get_records" && payload["docid"] == "instance-doc" {
						changed := runtime
						changed.TenantRoute = "other-source"
						data, _ := json.Marshal(changed)
						if err := os.WriteFile(configPath, data, 0600); err != nil {
							return nil, err
						}
						future := time.Now().Add(time.Second)
						if err := os.Chtimes(configPath, future, future); err != nil {
							return nil, err
						}
					}
					response = result["result"]
				}
				body, _ := json.Marshal(response)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			if tc.name == "verified employee" {
				server := New(configPath)
				originalRows := fake.records
				for _, state := range []string{"启用", "暂停", "停用"} {
					fake.records = []any{identitySubjectRecord("human-subject", "employee-one", "人员主体", state)}
					_, readErr := server.CallToolWithOAuthEmployee(context.Background(), "wecom_schema_status", json.RawMessage(`{}`), "employee-one")
					if (readErr == nil) != (state == "启用") {
						t.Fatalf("state %q reader error=%v", state, readErr)
					}
					if state != "启用" {
						bootstrap := json.RawMessage(`{"target_role":"Z-S09","operation":"add_records","records":[{}]}`)
						if _, bootstrapErr := server.CallToolWithOAuthEmployee(context.Background(), "wecom_record_apply", bootstrap, "employee-one"); bootstrapErr == nil {
							t.Fatalf("state %q bypassed status via registration", state)
						}
					}
				}
				fake.records = nil
				if _, readErr := server.CallToolWithOAuthEmployee(context.Background(), "wecom_schema_status", json.RawMessage(`{}`), "employee-one"); readErr != nil {
					t.Fatalf("unregistered active employee cannot read: %v", readErr)
				}
				fake.records = originalRows
			}
			arguments := map[string]any{"recipient_userid": "recipient-one", "text": "fake message", "idempotency_key": "oauth-message-test-key"}
			if tc.forged {
				arguments["identity_binding_id"] = "other-employee-binding"
			}
			raw, _ := json.Marshal(arguments)
			value, err := New(configPath).CallToolWithOAuthEmployee(context.Background(), "wecom_send_app_message", raw, tc.employee)
			if writes != tc.wantWrites || (err == nil) != (tc.wantWrites == 1) {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
			if tc.wantWrites == 1 {
				out := value.(map[string]any)
				for key, want := range map[string]string{"verified_initiator_userid": tc.employee, "business_operator_userid": tc.employee} {
					if out[key] != want {
						t.Fatalf("%s=%v want %s", key, out[key], want)
					}
				}
				if tc.name == "verified employee" && (out["verified_initiator_subject_record_id"] != "human-subject" || out["verified_execution_subject_record_id"] != "ai-subject") {
					t.Fatal("missing verified subjects")
				}
				if tc.name == "missing personnel" && (out["personnel_subject_missing"] != true || out["verified_initiator_subject_record_id"] != nil) {
					t.Fatal("fabricated a personnel subject")
				}
			}
		})
	}
}

func (f *oauthPersonnelFake) Request(_ context.Context, op string, raw any) (map[string]any, error) {
	f.calls++
	p := raw.(map[string]any)
	var result map[string]any
	switch {
	case op == "get_sheet" && p["docid"] == "registry":
		result = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "registry-sheet"}}}
	case op == "get_fields" && p["docid"] == "registry":
		fields := []any{}
		for _, key := range []string{"registry_key", "docid", "lifecycle_status"} {
			fields = append(fields, map[string]any{"field_title": key, "field_id": key})
		}
		result = map[string]any{"fields": fields}
	case op == "get_records" && p["docid"] == "registry":
		values := map[string]any{}
		for k, v := range map[string]string{"registry_key": "instance-key", "docid": "instance-doc", "lifecycle_status": "active"} {
			values[k] = []any{map[string]any{"text": v}}
		}
		result = map[string]any{"records": []any{map[string]any{"values": values}}}
	case op == "get_sheet" && p["docid"] == "instance-doc":
		result = map[string]any{"sheet_list": []any{map[string]any{"type": "smartsheet", "sheet_id": "personnel", "title": "Z-S09｜主体"}}}
	case op == "get_records" && p["docid"] == "instance-doc" && p["sheet_id"] == "personnel":
		records := filterFakePersonnel(f.records, p)
		result = map[string]any{"errcode": float64(0), "records": records, "has_more": false}
	default:
		return nil, fmt.Errorf("unexpected or cross-instance request")
	}
	return map[string]any{"result": result}, nil
}

func TestOAuthPersonnelResolvedOnlyFromInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.md")
	schema := ""
	for i := 1; i <= 9; i++ {
		schema += fmt.Sprintf("## Z-S0%d｜表\n| 测试 | field | FIELD_TYPE_TEXT |\n", i)
	}
	schema += "| 企业微信成员或责任人 | member | FIELD_TYPE_USER |\n| 主体类型 | type | FIELD_TYPE_SINGLE_SELECT |\n| 主体状态 | status | FIELD_TYPE_SINGLE_SELECT |\n"
	if err := os.WriteFile(path, []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Config{SchemaMirrorPath: path, SchemaSource: "local_compatibility", RegistryDocumentID: "registry", RegistryKey: "instance-key", APIWhitelist: map[string][]string{"read": {"get_sheet", "get_fields", "get_records"}}}
	for _, tc := range []struct {
		name   string
		rows   []any
		wantOK bool
	}{
		{"matching employee", []any{identitySubjectRecord("human", "employee-one", "人员主体", "启用"), identitySubjectRecord("ai", "employee-one", "AI 执行主体", "启用")}, true},
		{"missing employee", nil, false},
		{"different employee", []any{identitySubjectRecord("other", "employee-two", "人员主体", "启用")}, false},
		{"disabled employee", []any{identitySubjectRecord("human", "employee-one", "人员主体", "暂停")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &oauthPersonnelFake{records: tc.rows}
			identity, err := resolvePersonnelIdentity(context.Background(), runtime, fake, verifiedIdentity{UserID: "employee-one"})
			if (err == nil) != tc.wantOK {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			if tc.wantOK && (identity.UserID != "employee-one" || identity.SubjectRecordID != "human") {
				t.Fatal(identity)
			}
		})
	}
}

func TestOAuthRejectsCallerIdentityBeforeAccess(t *testing.T) {
	s := &Server{}
	for _, key := range []string{"identity_binding_id", "userid", "wecom_userid", "tenant", "tenant_route", "subject_record_id", "verified_actor_userid", "verified_initiator_userid", "verified_execution_subject_record_id"} {
		args, _ := json.Marshal(map[string]string{key: "attacker"})
		if _, err := s.CallToolWithOAuthEmployee(context.Background(), "wecom_record_apply", args, "employee-one"); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	if _, err := s.CallToolWithOAuthEmployee(context.Background(), "wecom_record_apply", json.RawMessage(`{}`), ""); err == nil {
		t.Fatal("accepted missing employee")
	}
}

func TestOAuthSchemasDoNotRequestLegacyIdentity(t *testing.T) {
	definitions, err := OAuthToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range definitions {
		if d.Name == "wecom_identity_binding_start" || d.Name == "wecom_identity_binding_confirm" || d.Name == "wecom_identity_binding_status" || schemaRequiresProperty(d.InputSchema, identityBindingArgument) {
			t.Fatalf("legacy identity exposed: %s", d.Name)
		}
	}
}
