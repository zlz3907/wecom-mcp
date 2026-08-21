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

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
	defaultQueryBytes = 24000
	maxQueryBytes     = 24000
)

func recordQueryToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"target_role"},
		"properties": map[string]any{
			"target_role": map[string]any{"type": "string", "enum": []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", "Z-S08", "Z-S09"}},
			"record_ids":  map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
			"filter_spec": map[string]any{"type": "object"},
			"sort":        map[string]any{"type": "array", "maxItems": 10, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"field_id"}, "properties": map[string]any{"field_id": map[string]any{"type": "string"}, "desc": map[string]any{"type": "boolean"}}}},
			"offset":      map[string]any{"type": "integer", "minimum": 0, "maximum": 10000000},
			"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": maxQueryLimit},
			"field_ids":   map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
			"compact":     map[string]any{"type": "boolean", "default": true},
			"max_bytes":   map[string]any{"type": "integer", "minimum": 1024, "maximum": maxQueryBytes, "default": defaultQueryBytes},
		},
	}
}

type recordQueryInput struct {
	TargetRole string           `json:"target_role"`
	RecordIDs  []string         `json:"record_ids"`
	FilterSpec map[string]any   `json:"filter_spec"`
	Sort       []map[string]any `json:"sort"`
	Offset     int              `json:"offset"`
	Limit      int              `json:"limit"`
	FieldIDs   []string         `json:"field_ids"`
	Compact    *bool            `json:"compact"`
	MaxBytes   int              `json:"max_bytes"`
}

func (s *Server) queryRecords(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input recordQueryInput
	if err := strictDecode(raw, &input, "target_role", "record_ids", "filter_spec", "sort", "offset", "limit", "field_ids", "compact", "max_bytes"); err != nil {
		return nil, err
	}
	if err := role(input.TargetRole); err != nil {
		return nil, err
	}
	if !runtime.Allows("get_records") {
		return nil, fmt.Errorf("实例白名单未允许 get_records")
	}
	if input.Offset < 0 || input.Offset > 10000000 {
		return nil, fmt.Errorf("offset 必须介于 0 和 10000000")
	}
	if input.Limit == 0 {
		input.Limit = defaultQueryLimit
	}
	if input.Limit < 1 || input.Limit > maxQueryLimit {
		return nil, fmt.Errorf("limit 必须介于 1 和 1000")
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = defaultQueryBytes
	}
	if input.MaxBytes < 1024 || input.MaxBytes > maxQueryBytes {
		return nil, fmt.Errorf("max_bytes 必须介于 1024 和 24000")
	}
	if input.Compact == nil {
		defaultCompact := true
		input.Compact = &defaultCompact
	}

	fields := schema.Roles[input.TargetRole]
	if len(fields) == 0 {
		return nil, fmt.Errorf("Schema 镜像缺少 %s 字段", input.TargetRole)
	}
	if err := validateQueryIDs(input.RecordIDs, 100, "record_ids"); err != nil {
		return nil, err
	}
	fieldIDs, err := validateQueryFieldIDs(fields, input.FieldIDs, "field_ids")
	if err != nil {
		return nil, err
	}
	filterSpec, err := validateAndNormalizeFilter(fields, input.FilterSpec)
	if err != nil {
		return nil, err
	}
	sortRules, err := normalizeQuerySort(fields, input.Sort)
	if err != nil {
		return nil, err
	}
	if filterSpec != nil && len(sortRules) > 0 {
		return nil, fmt.Errorf("filter_spec 与 sort 不能同时使用；请拆成两个只读查询")
	}

	target, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, input.TargetRole, runtime.Allows)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"docid": target.DocumentID, "sheet_id": target.SheetID,
		"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "offset": input.Offset, "limit": input.Limit,
	}
	if len(input.RecordIDs) > 0 {
		payload["record_ids"] = input.RecordIDs
	}
	if len(fieldIDs) > 0 {
		payload["field_ids"] = fieldIDs
	}
	if filterSpec != nil {
		payload["filter_spec"] = filterSpec
	}
	if len(sortRules) > 0 {
		payload["sort"] = sortRules
	}
	response, err := client.Request(ctx, "get_records", payload)
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	return compactQueryResult(response, input.TargetRole, input.Offset, input.MaxBytes, *input.Compact), nil
}

func validateQueryIDs(values []string, max int, name string) error {
	if len(values) > max {
		return fmt.Errorf("%s 最多 %d 个", name, max)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return fmt.Errorf("%s 含有无效标识", name)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s 不得重复", name)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateQueryFieldIDs(fields map[string]config.Field, values []string, name string) ([]string, error) {
	if err := validateQueryIDs(values, 100, name); err != nil {
		return nil, err
	}
	byID := map[string]struct{}{}
	for _, field := range fields {
		byID[field.ID] = struct{}{}
	}
	for _, value := range values {
		if _, ok := byID[value]; !ok {
			return nil, fmt.Errorf("%s 包含未登记字段 ID: %s", name, value)
		}
	}
	return values, nil
}

func normalizeQuerySort(fields map[string]config.Field, values []map[string]any) ([]map[string]any, error) {
	if len(values) > 10 {
		return nil, fmt.Errorf("sort 最多 10 条")
	}
	result := make([]map[string]any, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if len(value) == 0 {
			return nil, fmt.Errorf("sort 规则不能为空")
		}
		fieldID, ok := value["field_id"].(string)
		if !ok || strings.TrimSpace(fieldID) == "" {
			return nil, fmt.Errorf("sort.field_id 必须是已登记字段 ID")
		}
		field, ok := fieldByID(fields, fieldID)
		if !ok {
			return nil, fmt.Errorf("sort.field_id 未登记: %s", fieldID)
		}
		if _, ok := seen[fieldID]; ok {
			return nil, fmt.Errorf("sort 不得重复字段")
		}
		seen[fieldID] = struct{}{}
		desc, ok := value["desc"]
		if !ok {
			desc = false
		}
		if _, ok := desc.(bool); !ok {
			return nil, fmt.Errorf("sort.desc 必须是布尔值")
		}
		result = append(result, map[string]any{"field_title": field.Title, "desc": desc})
	}
	return result, nil
}

func fieldByID(fields map[string]config.Field, id string) (config.Field, bool) {
	for _, field := range fields {
		if field.ID == id {
			return field, true
		}
	}
	return config.Field{}, false
}

func validateAndNormalizeFilter(fields map[string]config.Field, raw map[string]any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("filter_spec 不能为空")
	}
	allowed := map[string]bool{"conjunction": true, "conditions": true}
	for key := range raw {
		if !allowed[key] {
			return nil, fmt.Errorf("filter_spec 不支持字段: %s", key)
		}
	}
	conjunction, ok := raw["conjunction"].(string)
	if !ok || (conjunction != "CONJUNCTION_AND" && conjunction != "CONJUNCTION_OR") {
		return nil, fmt.Errorf("filter_spec.conjunction 必须是 CONJUNCTION_AND 或 CONJUNCTION_OR")
	}
	conditions, ok := raw["conditions"].([]any)
	if !ok || len(conditions) == 0 || len(conditions) > 20 {
		return nil, fmt.Errorf("filter_spec.conditions 必须包含 1 到 20 条条件")
	}
	resultConditions := make([]any, 0, len(conditions))
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("filter_spec.conditions 条件形态无效")
		}
		normalized, err := normalizeFilterCondition(fields, condition)
		if err != nil {
			return nil, err
		}
		resultConditions = append(resultConditions, normalized)
	}
	return map[string]any{"conjunction": conjunction, "conditions": resultConditions}, nil
}

func normalizeFilterCondition(fields map[string]config.Field, condition map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"field_id": true, "field_type": true, "operator": true, "string_value": true, "number_value": true, "bool_value": true, "date_time_value": true, "user_value": true}
	for key := range condition {
		if !allowed[key] {
			return nil, fmt.Errorf("过滤条件不支持字段: %s", key)
		}
	}
	fieldID, ok := condition["field_id"].(string)
	if !ok || strings.TrimSpace(fieldID) == "" {
		return nil, fmt.Errorf("过滤条件必须提供已登记 field_id")
	}
	field, ok := fieldByID(fields, fieldID)
	if !ok {
		return nil, fmt.Errorf("过滤条件 field_id 未登记: %s", fieldID)
	}
	if supplied, ok := condition["field_type"].(string); ok && supplied != field.Type {
		return nil, fmt.Errorf("过滤条件 field_type 与 Schema 不匹配: %s", fieldID)
	}
	operator, ok := condition["operator"].(string)
	if !ok || !validFilterOperators[operator] {
		return nil, fmt.Errorf("过滤条件 operator 不受支持")
	}
	result := map[string]any{"field_id": fieldID, "field_type": field.Type, "operator": operator}
	for _, key := range []string{"string_value", "number_value", "bool_value", "date_time_value", "user_value"} {
		if value, exists := condition[key]; exists {
			if err := validateFilterValue(key, value); err != nil {
				return nil, err
			}
			result[key] = value
		}
	}
	if !valueBearingOperator(operator) && len(result) != 3 {
		return nil, fmt.Errorf("操作符 %s 不应携带值", operator)
	}
	if valueBearingOperator(operator) && len(result) == 3 {
		return nil, fmt.Errorf("操作符 %s 必须携带值", operator)
	}
	return result, nil
}

var validFilterOperators = map[string]bool{
	"OPERATOR_IS": true, "OPERATOR_IS_NOT": true, "OPERATOR_CONTAINS": true, "OPERATOR_DOES_NOT_CONTAIN": true,
	"OPERATOR_IS_GREATER": true, "OPERATOR_IS_GREATER_OR_EQUAL": true, "OPERATOR_IS_LESS": true, "OPERATOR_IS_LESS_OR_EQUAL": true,
	"OPERATOR_IS_EMPTY": true, "OPERATOR_IS_NOT_EMPTY": true,
}

func valueBearingOperator(operator string) bool {
	return operator != "OPERATOR_IS_EMPTY" && operator != "OPERATOR_IS_NOT_EMPTY"
}

func validateFilterValue(name string, value any) error {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return fmt.Errorf("过滤条件 %s 必须是单值对象", name)
	}
	switch name {
	case "string_value", "user_value":
		items, ok := object["value"].([]any)
		if !ok || len(items) == 0 || len(items) > 20 {
			return fmt.Errorf("过滤条件 %s.value 必须是 1 到 20 个字符串", name)
		}
		for _, item := range items {
			if text, ok := item.(string); !ok || strings.TrimSpace(text) == "" {
				return fmt.Errorf("过滤条件 %s.value 必须是非空字符串数组", name)
			}
		}
	case "number_value":
		if _, ok := object["value"].(float64); !ok {
			return fmt.Errorf("过滤条件 number_value.value 必须是数字")
		}
	case "bool_value":
		if _, ok := object["value"].(bool); !ok {
			return fmt.Errorf("过滤条件 bool_value.value 必须是布尔值")
		}
	case "date_time_value":
		kind, ok := object["type"].(string)
		if !ok || !strings.HasPrefix(kind, "DATE_TIME_TYPE_") {
			return fmt.Errorf("过滤条件 date_time_value.type 无效")
		}
	default:
		return fmt.Errorf("过滤条件值类型不受支持: %s", name)
	}
	return nil
}

func compactQueryResult(response map[string]any, targetRole string, offset, maxBytes int, compact bool) map[string]any {
	result, _ := response["result"].(map[string]any)
	records, _ := result["records"].([]any)
	total := len(records)
	if value, ok := result["total"].(float64); ok && value >= 0 {
		total = int(value)
	}
	hasMore, _ := result["has_more"].(bool)
	nextOffset := offset + len(records)
	if value, ok := result["next"].(float64); ok && value >= 0 {
		nextOffset = int(value)
	}
	outputRecords := make([]any, 0, len(records))
	for _, record := range records {
		if compact {
			outputRecords = append(outputRecords, compactQueryRecord(record))
		} else {
			outputRecords = append(outputRecords, record)
		}
	}
	output := map[string]any{"target_role": targetRole, "record_count": total, "returned_count": len(outputRecords), "has_more": hasMore, "records": outputRecords, "response_truncated": false}
	if hasMore {
		output["next_offset"] = nextOffset
	}
	for len(mustMarshal(output)) > maxBytes && len(outputRecords) > 0 {
		outputRecords = outputRecords[:len(outputRecords)-1]
		output["records"] = outputRecords
		output["returned_count"] = len(outputRecords)
		output["response_truncated"] = true
		output["has_more"] = true
		output["next_offset"] = offset + len(outputRecords)
	}
	return output
}

func compactQueryRecord(value any) map[string]any {
	record, _ := value.(map[string]any)
	output := map[string]any{}
	for _, key := range []string{"record_id", "create_time", "update_time"} {
		if record[key] != nil {
			output[key] = record[key]
		}
	}
	values, _ := record["values"].(map[string]any)
	compactValues := map[string]any{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		compactValues[key] = compactCell(values[key])
	}
	output["values"] = compactValues
	return output
}

func compactCell(value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			for _, key := range []string{"text", "id", "value", "user_id"} {
				if selected, exists := object[key]; exists {
					result = append(result, selected)
					break
				}
			}
			continue
		}
		result = append(result, item)
	}
	return result
}

func mustMarshal(value any) []byte { data, _ := json.Marshal(value); return data }
