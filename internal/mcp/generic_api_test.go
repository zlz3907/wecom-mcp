package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type employeeListFake struct {
	called bool
}

func (fake *employeeListFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	fake.called = true
	if operation != "list_employees" {
		return nil, nil
	}
	return map[string]any{"result": map[string]any{
		"errcode": float64(0), "errmsg": "ok",
		"userlist": []any{map[string]any{
			"userid": "employee-1", "name": "示例员工", "department": []any{float64(1)},
			"position": "设计师", "status": float64(1), "mobile": "not-returned", "email": "not-returned@example.com",
		}},
	}}, nil
}

func TestEmployeeListIsAReaderToolWithNoCallerRouting(t *testing.T) {
	definitions, err := ToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.Name != "wecom_employee_list" {
			continue
		}
		if definition.Access != ToolAccessReader {
			t.Fatalf("access=%s, want reader", definition.Access)
		}
		encoded, err := json.Marshal(definition.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{"additionalProperties":false,"type":"object"}` {
			t.Fatalf("employee list unexpectedly accepts routing input: %s", encoded)
		}
		return
	}
	t.Fatal("wecom_employee_list is not published")
}

func TestEmployeeListReturnsOnlyBasicFields(t *testing.T) {
	server := &Server{}
	client := &employeeListFake{}
	runtime := config.Config{APIWhitelist: map[string][]string{"directory": {"list_employees"}}}
	value, err := server.listEmployees(context.Background(), runtime, client, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["employee_count"] != 1 || result["scope"] != "current_fixed_tenant_root_with_children" {
		t.Fatalf("directory metadata missing: %#v", result)
	}
	employee := result["employees"].([]map[string]any)[0]
	if employee["userid"] != "employee-1" || employee["name"] != "示例员工" {
		t.Fatalf("basic employee fields missing: %#v", employee)
	}
	if employee["mobile"] != nil || employee["email"] != nil {
		t.Fatalf("unexpected contact details escaped sanitization: %#v", employee)
	}
}

func TestEmployeeListStillUsesMCPInstanceWhitelist(t *testing.T) {
	server := &Server{}
	client := &employeeListFake{}
	if _, err := server.listEmployees(context.Background(), config.Config{APIWhitelist: map[string][]string{"read": {"get_records"}}}, client, json.RawMessage(`{}`)); err == nil {
		t.Fatal("employee list must remain controlled by the fixed MCP instance")
	}
	if client.called {
		t.Fatal("denied employee list reached the upstream API")
	}
}

func TestGetEmployeeValidationAndProjection(t *testing.T) {
	if _, err := validateLegacyOperation("get_employee", map[string]any{"userid": "operator@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLegacyOperation("get_employee", map[string]any{"userid": "operator", "url": "https://other.invalid"}); err == nil {
		t.Fatal("accepted caller routing")
	}
	got := sanitizeLegacyResponse("get_employee", map[string]any{"errcode": 0, "userid": "operator", "status": 1, "mobile": "private", "email": "private", "errmsg": "private", "name": "private"}).(map[string]any)
	if len(got) != 3 || got["userid"] != "operator" || got["status"] != 1 || got["errcode"] != 0 {
		t.Fatalf("unexpected projection: %#v", got)
	}
}
