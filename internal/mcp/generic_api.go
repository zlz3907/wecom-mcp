package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

// genericAPICall exposes the legacy MCP's fixed-tenant compatibility surface.
// Online structure mutations are deliberately excluded: they must go through
// the catalogued admin migration flow with preview binding and readback.
func (s *Server) genericAPICall(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input struct {
		Operation string         `json:"operation"`
		Payload   map[string]any `json:"payload"`
	}
	if err := strictDecode(raw, &input, "operation", "payload"); err != nil {
		return nil, err
	}
	definition, exists := wecom.Operations[input.Operation]
	if !exists || input.Operation == "get_sheet" {
		return nil, fmt.Errorf("operation 必须是旧 MCP 已登记的企业微信 API")
	}
	if definition.Kind == "write" {
		return nil, fmt.Errorf("写操作必须使用受管记录工具或管理员 Schema 迁移工具")
	}
	if !runtime.Allows(input.Operation) {
		return nil, fmt.Errorf("实例白名单未允许 %s", input.Operation)
	}
	payload, err := validateLegacyOperation(input.Operation, input.Payload)
	if err != nil {
		return nil, err
	}
	result, err := client.Request(ctx, input.Operation, payload)
	if err != nil {
		return nil, err
	}
	if err := apiError(result); err != nil {
		return nil, err
	}
	return map[string]any{"operation": input.Operation, "kind": definition.Kind, "result": sanitizeLegacyResponse(input.Operation, result["result"])}, nil
}

func schemaMutationOperation(operation string) bool {
	return map[string]bool{
		"add_sheet": true, "update_sheet": true, "delete_sheet": true,
		"add_view": true, "update_view": true, "delete_views": true,
		"add_fields": true, "update_fields": true, "delete_fields": true,
	}[operation]
}

func validateLegacyOperation(operation string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	if containsReservedRequestField(payload, false) {
		return nil, fmt.Errorf("请求不得包含租户、地址或凭据路由字段")
	}
	if operation == "list_employees" {
		if len(payload) != 0 {
			return nil, fmt.Errorf("list_employees 不接受参数")
		}
		return payload, nil
	}
	if operation == "get_doc_base_info" || operation == "get_doc_share_url" || operation == "get_doc_auth" {
		if len(payload) != 1 || !identifier(payload["docid"]) {
			return nil, fmt.Errorf("%s 仅接受非空 docid", operation)
		}
		return payload, nil
	}
	if operation == "create_smartsheet" {
		name, _ := payload["doc_name"].(string)
		if len(payload) != 2 || payload["doc_type"] != float64(10) && payload["doc_type"] != 10 || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("新建智能表格仅接受 doc_type: 10 与非空 doc_name")
		}
		return payload, nil
	}
	if operation == "rename_document" {
		name, _ := payload["new_name"].(string)
		if len(payload) != 2 || !identifier(payload["docid"]) || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("重命名仅接受 docid 与非空 new_name")
		}
		return payload, nil
	}
	if operation == "add_fields" || operation == "update_fields" {
		if values, exists := payload["field_list"]; exists && payload["fields"] == nil {
			payload["fields"] = values
			delete(payload, "field_list")
		}
		if !identifier(payload["docid"]) || !identifier(payload["sheet_id"]) {
			return nil, fmt.Errorf("字段操作必须提供 docid 与 sheet_id")
		}
		if _, ok := payload["fields"].([]any); !ok {
			return nil, fmt.Errorf("字段操作必须提供 fields 数组")
		}
		return payload, nil
	}
	if operation == "delete_fields" || operation == "delete_sheet" || operation == "delete_views" || operation == "delete_records" {
		if !identifier(payload["docid"]) || !identifier(payload["sheet_id"]) {
			return nil, fmt.Errorf("删除操作必须提供 docid 与 sheet_id")
		}
		return payload, nil
	}
	if strings.HasPrefix(operation, "get_") || strings.HasSuffix(operation, "_sheet") || strings.HasSuffix(operation, "_view") || strings.HasSuffix(operation, "_records") {
		if !identifier(payload["docid"]) {
			return nil, fmt.Errorf("%s 必须提供 docid", operation)
		}
		if operation != "get_sheets" && !identifier(payload["sheet_id"]) {
			return nil, fmt.Errorf("%s 必须提供 sheet_id", operation)
		}
	}
	if operation == "add_records" || operation == "update_records" {
		records, ok := payload["records"].([]any)
		if !ok || len(records) == 0 {
			return nil, fmt.Errorf("记录操作必须提供 records")
		}
		for _, item := range records {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("记录形态无效")
			}
			if _, ok := record["values"].(map[string]any); !ok {
				return nil, fmt.Errorf("记录必须提供对象形式 values")
			}
		}
		payload["key_type"] = "CELL_VALUE_KEY_TYPE_FIELD_ID"
	}
	return payload, nil
}

func identifier(value any) bool {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > 256 {
		return false
	}
	for _, char := range text {
		if !(char == '_' || char == '-' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
			return false
		}
	}
	return true
}

func containsReservedRequestField(value any, recordValues bool) bool {
	if list, ok := value.([]any); ok {
		for _, item := range list {
			if containsReservedRequestField(item, recordValues) {
				return true
			}
		}
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, child := range object {
		normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
		if !recordValues && map[string]bool{"authorization": true, "accesstoken": true, "corpsecret": true, "provider": true, "providercode": true, "credential": true, "credentialref": true, "host": true, "hostname": true, "url": true, "uri": true, "baseurl": true, "upstreamhost": true}[normalized] {
			return true
		}
		if containsReservedRequestField(child, recordValues || key == "values") {
			return true
		}
	}
	return false
}

func sanitizeLegacyResponse(operation string, value any) any {
	if operation == "list_employees" {
		return sanitizeEmployees(value)
	}
	return stripSensitive(value)
}

func sanitizeEmployees(value any) map[string]any {
	response, _ := value.(map[string]any)
	output := map[string]any{}
	for _, key := range []string{"errcode", "errmsg"} {
		if response[key] != nil {
			output[key] = response[key]
		}
	}
	users, _ := response["userlist"].([]any)
	employees := make([]map[string]any, 0, len(users))
	for _, item := range users {
		user, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]any{}
		for _, key := range []string{"userid", "name", "department", "position", "status"} {
			if user[key] != nil {
				row[key] = user[key]
			}
		}
		employees = append(employees, row)
	}
	output["employees"] = employees
	return output
}

func stripSensitive(value any) any {
	if list, ok := value.([]any); ok {
		output := make([]any, 0, len(list))
		for _, item := range list {
			output = append(output, stripSensitive(item))
		}
		return output
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	output := map[string]any{}
	for key, child := range object {
		normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
		if map[string]bool{"authorization": true, "accesstoken": true, "corpsecret": true, "provider": true, "providercode": true, "credential": true, "credentialref": true}[normalized] {
			continue
		}
		output[key] = stripSensitive(child)
	}
	return output
}

func legacyOperations() []string {
	names := make([]string, 0, len(wecom.Operations))
	for name, definition := range wecom.Operations {
		if name != "get_sheet" && definition.Kind == "read" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
