package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
		definitions = append(definitions, ToolDefinition{
			Name:        item.Name,
			Description: item.Description,
			InputSchema: item.InputSchema,
			Access:      access,
		})
	}
	return definitions, nil
}

// CallTool lets an additional MCP transport reuse the same fixed-tenant
// business implementation as stdio without bypassing its validation.
func (s *Server) CallTool(ctx context.Context, name string, arguments json.RawMessage) (any, error) {
	return s.call(ctx, name, arguments)
}
