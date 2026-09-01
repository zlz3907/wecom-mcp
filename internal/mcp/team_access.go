package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

// ToolAccess is the minimum team role required to discover and call a tool.
// Roles are intentionally transport-facing; the existing fixed-tenant and
// operation allowlist checks remain the final business authorization layer.
type ToolAccess string

const (
	ToolAccessReader   ToolAccess = "reader"
	ToolAccessOperator ToolAccess = "operator"
	ToolAccessAdmin    ToolAccess = "admin"
)

// ToolDefinition is the transport-neutral MCP tool contract exposed by the
// existing stdio server and reused by the remote team transport.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema any
	Access      ToolAccess
}

var teamToolAccess = map[string]ToolAccess{
	"wecom_instance_initialize":                   ToolAccessAdmin,
	"wecom_instance_initialize_status":            ToolAccessReader,
	"wecom_instance_initialize_apply":             ToolAccessAdmin,
	"wecom_registry_bootstrap":                    ToolAccessAdmin,
	"wecom_schema_status":                         ToolAccessReader,
	"wecom_schema_probe":                          ToolAccessReader,
	"wecom_schema_sync":                           ToolAccessAdmin,
	"wecom_field_codec_lab_create":                ToolAccessAdmin,
	"wecom_field_codec_lab_read":                  ToolAccessReader,
	"wecom_field_codec_lab_reference_debug":       ToolAccessReader,
	"wecom_field_codec_lab_reference_write_probe": ToolAccessAdmin,
	"wecom_field_codec_lab_write_probe":           ToolAccessAdmin,
	"wecom_field_codec_lab_replay_probe":          ToolAccessAdmin,
	"wecom_field_codec_lab_registry_status":       ToolAccessReader,
	"wecom_field_codec_lab_register":              ToolAccessAdmin,
	"wecom_api_call":                              ToolAccessAdmin,
	"wecom_identity_binding_start":                ToolAccessOperator,
	"wecom_identity_binding_confirm":              ToolAccessOperator,
	"wecom_identity_binding_status":               ToolAccessReader,
	"wecom_send_app_message":                      ToolAccessOperator,
	"wecom_record_read":                           ToolAccessReader,
	"wecom_record_query":                          ToolAccessReader,
	"wecom_record_apply":                          ToolAccessOperator,
	"wecom_requirement_progress_reconcile":        ToolAccessOperator,
	"wecom_schema_migration_preview":              ToolAccessAdmin,
	"wecom_schema_migration_apply":                ToolAccessAdmin,
}

// ToolDefinitions returns a copy of the current transport-neutral tool list.
// A missing access classification fails closed so a newly added privileged
// tool cannot accidentally become available to remote team callers.
func ToolDefinitions() ([]ToolDefinition, error) {
	definitions := make([]ToolDefinition, 0, len(tools))
	for _, item := range tools {
		access, ok := teamToolAccess[item.Name]
		if !ok {
			return nil, fmt.Errorf("工具 %s 缺少团队权限分类", item.Name)
		}
		inputSchema := item.InputSchema
		description := item.Description
		if requiresTeamIdentityBinding(item.Name, access) {
			var err error
			inputSchema, err = identityBoundToolSchema(item.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("工具 %s 身份绑定参数注入失败", item.Name)
			}
			description += " 远程团队调用必须提供已完成企业微信验证码验证的 identity_binding_id；缺失时先调用 wecom_identity_binding_start 与 wecom_identity_binding_confirm。"
		}
		definitions = append(definitions, ToolDefinition{
			Name:        item.Name,
			Description: description,
			InputSchema: inputSchema,
			Access:      access,
		})
	}
	return definitions, nil
}

// CallTool lets an additional MCP transport reuse the same fixed-tenant
// business implementation as stdio without bypassing its validation.
func (s *Server) CallTool(ctx context.Context, name string, arguments json.RawMessage) (any, error) {
	access, classified := teamToolAccess[name]
	if !classified {
		return nil, fmt.Errorf("未知或未分类工具: %s", name)
	}
	if !requiresTeamIdentityBinding(name, access) {
		return s.call(ctx, name, arguments)
	}
	runtime, err := s.store.Current()
	if err != nil {
		return nil, err
	}
	identity, cleaned, err := s.verifyAndStripTeamIdentityBinding(runtime, arguments)
	if err != nil {
		return nil, err
	}
	executionSubject, err := configuredAIExecutionSubject(runtime)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, verifiedIdentityContextKey{}, identity)
	ctx = context.WithValue(ctx, verifiedExecutionSubjectContextKey{}, executionSubject)
	value, err := s.call(ctx, name, cleaned)
	if err != nil {
		return nil, err
	}
	if output, ok := value.(map[string]any); ok {
		// Keep the verified_actor fields for existing clients while exposing the
		// initiator/executor split explicitly for Zoop governance.
		output["verified_actor_userid"] = identity.UserID
		output["verified_actor_name"] = identity.DisplayName
		output["verified_actor_subject_record_id"] = identity.SubjectRecordID
		output["verified_initiator_userid"] = identity.UserID
		output["verified_initiator_name"] = identity.DisplayName
		output["verified_initiator_subject_record_id"] = identity.SubjectRecordID
		output["verified_execution_subject_record_id"] = executionSubject.RecordID
		output["identity_binding_verified"] = true
	}
	return value, nil
}

func requiresTeamIdentityBinding(name string, access ToolAccess) bool {
	if access == ToolAccessReader {
		return false
	}
	return name != "wecom_identity_binding_start" && name != "wecom_identity_binding_confirm"
}

func identityBoundToolSchema(schema any) (any, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if json.Unmarshal(encoded, &object) != nil || object["type"] != "object" {
		return nil, fmt.Errorf("tool schema is not an object")
	}
	properties, ok := object["properties"].(map[string]any)
	if !ok {
		properties = map[string]any{}
		object["properties"] = properties
	}
	properties[identityBindingArgument] = map[string]any{
		"type": "string", "minLength": 32, "maxLength": 128,
		"description": "企业微信验证码确认后得到的永久绑定句柄；不可使用团队 API Key、姓名或 userid 代替。",
	}
	required, _ := object["required"].([]any)
	for _, item := range required {
		if item == identityBindingArgument {
			return object, nil
		}
	}
	object["required"] = append(required, identityBindingArgument)
	return object, nil
}

func (s *Server) verifyAndStripTeamIdentityBinding(runtime config.Config, arguments json.RawMessage) (verifiedIdentity, json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(arguments, &object) != nil || object == nil {
		return verifiedIdentity{}, nil, fmt.Errorf("工具参数必须是对象")
	}
	var bindingID string
	if json.Unmarshal(object[identityBindingArgument], &bindingID) != nil {
		return verifiedIdentity{}, nil, fmt.Errorf("远程写入前必须先完成企业微信身份绑定")
	}
	identity, _, err := s.resolveIdentityBinding(runtime, bindingID)
	if err != nil {
		return verifiedIdentity{}, nil, err
	}
	delete(object, identityBindingArgument)
	cleaned, err := json.Marshal(object)
	if err != nil {
		return verifiedIdentity{}, nil, fmt.Errorf("清理身份绑定参数失败")
	}
	return identity, cleaned, nil
}
