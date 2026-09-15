package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestEveryToolHasTeamAccessClassification(t *testing.T) {
	definitions, err := ToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != len(tools) {
		t.Fatalf("definitions=%d tools=%d", len(definitions), len(tools))
	}
	access := make(map[string]ToolAccess, len(definitions))
	for _, definition := range definitions {
		access[definition.Name] = definition.Access
	}
	for name, want := range map[string]ToolAccess{
		"wecom_identity_binding_start":   ToolAccessOperator,
		"wecom_identity_binding_confirm": ToolAccessOperator,
		"wecom_identity_binding_status":  ToolAccessReader,
		"wecom_employee_list":            ToolAccessReader,
		"wecom_record_query":             ToolAccessReader,
		"wecom_record_apply":             ToolAccessOperator,
		"wecom_send_app_message":         ToolAccessOperator,
		"wecom_registry_bootstrap":       ToolAccessAdmin,
		"wecom_schema_migration_apply":   ToolAccessAdmin,
	} {
		if got := access[name]; got != want {
			t.Fatalf("%s access=%q, want %q", name, got, want)
		}
	}
}

func TestReaderAndOperatorToolSchemasHaveNoNestedBareObjects(t *testing.T) {
	definitions, err := ToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.Access == ToolAccessAdmin {
			continue
		}
		if err := rejectNestedBareObject(definition.InputSchema, definition.Name, true); err != nil {
			t.Fatal(err)
		}
	}
}

func rejectNestedBareObject(value any, path string, root bool) error {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if object["type"] == "object" {
		_, hasProperties := object["properties"]
		_, hasAdditionalProperties := object["additionalProperties"]
		_, hasOneOf := object["oneOf"]
		_, hasAnyOf := object["anyOf"]
		if !root && !hasProperties && !hasAdditionalProperties && !hasOneOf && !hasAnyOf {
			return fmt.Errorf("%s publishes a nested object without a contract", path)
		}
	}
	for key, child := range object {
		switch typed := child.(type) {
		case map[string]any:
			if err := rejectNestedBareObject(typed, path+"."+key, false); err != nil {
				return err
			}
		case []any:
			for index, item := range typed {
				if err := rejectNestedBareObject(item, fmt.Sprintf("%s.%s[%d]", path, key, index), false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func TestTeamSchemasRequireIdentityExceptBindingBootstrap(t *testing.T) {
	definitions, err := ToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]ToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	for _, name := range []string{"wecom_record_apply", "wecom_send_app_message", "wecom_schema_migration_apply"} {
		if !schemaRequiresProperty(byName[name].InputSchema, identityBindingArgument) {
			t.Fatalf("%s does not require %s", name, identityBindingArgument)
		}
	}
	for _, name := range []string{"wecom_identity_binding_start", "wecom_identity_binding_confirm"} {
		if schemaRequiresProperty(byName[name].InputSchema, identityBindingArgument) && name == "wecom_identity_binding_start" {
			t.Fatalf("%s cannot require an existing binding", name)
		}
	}
}

func schemaRequiresProperty(raw any, property string) bool {
	schema, _ := raw.(map[string]any)
	required, _ := schema["required"].([]string)
	if len(required) == 0 {
		generic, _ := schema["required"].([]any)
		for _, item := range generic {
			if item == property {
				return true
			}
		}
	}
	for _, name := range required {
		if name == property {
			return true
		}
	}
	return false
}

func TestVerifyAndStripTeamIdentityBinding(t *testing.T) {
	t.Setenv(identityBindingSecretEnv, "0123456789abcdef0123456789abcdef")
	runtime := config.Config{InstanceName: "team-test", StatePath: filepath.Join(t.TempDir(), "state.json")}
	bindingID := identityBindingID([]byte("0123456789abcdef0123456789abcdef"), runtime, "identity-team-key-0001")
	state := identityBindingState{Version: identityBindingStateVersion, Bindings: map[string]identityBindingEntry{
		identityBindingLookupKey(bindingID): {
			ActiveUserID: "user-one", ActiveDisplayName: "第一人", ActiveSubjectRecordID: "subject-one", Generation: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}}
	if err := saveIdentityBindingState(identityBindingStatePath(runtime), state); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	identity, raw, err := server.verifyAndStripTeamIdentityBinding(runtime, json.RawMessage(`{"identity_binding_id":"`+bindingID+`","target_role":"Z-S01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if identity.UserID != "user-one" || identity.SubjectRecordID != "subject-one" {
		t.Fatalf("verified identity was not resolved: %#v", identity)
	}
	if string(raw) != `{"target_role":"Z-S01"}` {
		t.Fatalf("reserved binding argument was not stripped: %s", raw)
	}
	if _, _, err := server.verifyAndStripTeamIdentityBinding(runtime, json.RawMessage(`{"target_role":"Z-S01"}`)); err == nil {
		t.Fatal("missing identity binding was accepted")
	}
}
