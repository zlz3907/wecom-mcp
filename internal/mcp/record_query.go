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
	conditionProperties := map[string]any{
		"field_title": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Z-S00 active generation 中的字段标题。与 field_id 二选一；推荐使用标题，服务器会解析并校验真实 field_id。"},
		"field_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Z-S00 active generation 中的真实字段 ID。与 field_title 二选一。"},
		"field_type":  map[string]any{"type": "string", "pattern": "^FIELD_TYPE_[A-Z0-9_]+$", "description": "可选校验值；提供时必须与 Z-S00 active generation 一致。"},
		"operator": map[string]any{"type": "string", "enum": []string{
			"OPERATOR_IS", "OPERATOR_IS_NOT", "OPERATOR_CONTAINS", "OPERATOR_DOES_NOT_CONTAIN",
			"OPERATOR_IS_GREATER", "OPERATOR_IS_GREATER_OR_EQUAL", "OPERATOR_IS_LESS", "OPERATOR_IS_LESS_OR_EQUAL",
			"OPERATOR_IS_EMPTY", "OPERATOR_IS_NOT_EMPTY",
		}},
		"string_value":    map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]any{"type": "string", "minLength": 1}}}},
		"number_value":    map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "number"}}},
		"bool_value":      map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "boolean"}}},
		"date_time_value": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"type": "string", "pattern": "^DATE_TIME_TYPE_"}}},
		"user_value":      map[string]any{"type": "object", "additionalProperties": false, "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]any{"type": "string", "minLength": 1}}}},
	}
	filterSpec := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"conjunction", "conditions"},
		"properties": map[string]any{
			"conjunction": map[string]any{"type": "string", "enum": []string{"CONJUNCTION_AND", "CONJUNCTION_OR"}},
			"conditions": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"operator"}, "properties": conditionProperties,
				"oneOf": []any{
					map[string]any{"required": []string{"field_title"}, "not": map[string]any{"required": []string{"field_id"}}},
					map[string]any{"required": []string{"field_id"}, "not": map[string]any{"required": []string{"field_title"}}},
				},
				"description": "必须提供 field_title 或 field_id 之一。IS/比较/包含操作符必须携带一个与字段类型对应的 *_value；IS_EMPTY/IS_NOT_EMPTY 不携带值。",
			}},
		},
	}
	sortItem := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"field_title": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "与 field_id 二选一；推荐使用本地 Schema 字段标题。"},
			"field_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "与 field_title 二选一。"},
			"desc":        map[string]any{"type": "boolean", "default": false},
		},
		"oneOf": []any{
			map[string]any{"required": []string{"field_title"}, "not": map[string]any{"required": []string{"field_id"}}},
			map[string]any{"required": []string{"field_id"}, "not": map[string]any{"required": []string{"field_title"}}},
		},
		"description": "必须提供 field_title 或 field_id 之一。",
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"target_role"},
		"properties": map[string]any{
			"target_role":          map[string]any{"type": "string", "enum": []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", "Z-S08", "Z-S09"}},
			"record_ids":           map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
			"filter_spec":          filterSpec,
			"sort":                 map[string]any{"type": "array", "maxItems": 10, "items": sortItem, "description": "不能与 filter_spec 同时使用。"},
			"offset":               map[string]any{"type": "integer", "minimum": 0, "maximum": 10000000},
			"limit":                map[string]any{"type": "integer", "minimum": 1, "maximum": maxQueryLimit},
			"field_ids":            map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "description": "字段投影，与 field_titles 二选一。"},
			"field_titles":         map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "description": "按运行时 Schema 字段标题投影，与 field_ids 二选一。"},
			"compact":              map[string]any{"type": "boolean", "default": true},
			"include_empty_fields": map[string]any{"type": "boolean", "default": false, "description": "仅用于 compact=true；将本次 field_ids/field_titles 投影中未出现在上游记录 values 的字段补为 null。必须显式提供字段投影，避免整表宽响应。"},
			"max_bytes":            map[string]any{"type": "integer", "minimum": 1024, "maximum": maxQueryBytes, "default": defaultQueryBytes},
		},
	}
}

type recordQueryInput struct {
	TargetRole         string           `json:"target_role"`
	RecordIDs          []string         `json:"record_ids"`
	FilterSpec         map[string]any   `json:"filter_spec"`
	Sort               []map[string]any `json:"sort"`
	Offset             int              `json:"offset"`
	Limit              int              `json:"limit"`
	FieldIDs           []string         `json:"field_ids"`
	FieldTitles        []string         `json:"field_titles"`
	Compact            *bool            `json:"compact"`
	IncludeEmptyFields bool             `json:"include_empty_fields"`
	MaxBytes           int              `json:"max_bytes"`
}

func (s *Server) queryRecords(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input recordQueryInput
	if err := strictDecode(raw, &input, "target_role", "record_ids", "filter_spec", "sort", "offset", "limit", "field_ids", "field_titles", "compact", "include_empty_fields", "max_bytes"); err != nil {
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
		return nil, fmt.Errorf("Z-S00 Schema 缺少 %s 字段", input.TargetRole)
	}
	if err := validateQueryIDs(input.RecordIDs, 100, "record_ids"); err != nil {
		return nil, err
	}
	fieldIDs, err := resolveQueryProjection(fields, input.FieldIDs, input.FieldTitles)
	if err != nil {
		return nil, err
	}
	if err := validateEmptyFieldProjection(input.IncludeEmptyFields, *input.Compact, fieldIDs); err != nil {
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
	return compactQueryResult(response, input.TargetRole, input.Offset, input.MaxBytes, *input.Compact, emptyFieldProjection(input.IncludeEmptyFields, fieldIDs)), nil
}

func emptyFieldProjection(include bool, fieldIDs []string) []string {
	if !include {
		return nil
	}
	return fieldIDs
}

func validateEmptyFieldProjection(include, compact bool, fieldIDs []string) error {
	if !include {
		return nil
	}
	if !compact {
		return fmt.Errorf("include_empty_fields 仅支持 compact=true")
	}
	if len(fieldIDs) == 0 {
		return fmt.Errorf("include_empty_fields=true 时必须提供 field_ids 或 field_titles")
	}
	return nil
}

func resolveQueryProjection(fields map[string]config.Field, fieldIDs, fieldTitles []string) ([]string, error) {
	if len(fieldIDs) > 0 && len(fieldTitles) > 0 {
		return nil, fmt.Errorf("field_ids 与 field_titles 不能同时使用")
	}
	if len(fieldTitles) == 0 {
		return validateQueryFieldIDs(fields, fieldIDs, "field_ids")
	}
	if err := validateQueryIDs(fieldTitles, 100, "field_titles"); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(fieldTitles))
	for _, title := range fieldTitles {
		field, ok := fields[title]
		if !ok {
			return nil, fmt.Errorf("field_titles 包含未登记字段标题: %s", title)
		}
		result = append(result, field.ID)
	}
	return result, nil
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
		field, err := resolveQueryField(fields, value, "sort")
		if err != nil {
			return nil, err
		}
		fieldID := field.ID
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
	allowed := map[string]bool{"field_title": true, "field_id": true, "field_type": true, "operator": true, "string_value": true, "number_value": true, "bool_value": true, "date_time_value": true, "user_value": true}
	for key := range condition {
		if !allowed[key] {
			return nil, fmt.Errorf("过滤条件不支持字段: %s", key)
		}
	}
	field, err := resolveQueryField(fields, condition, "过滤条件")
	if err != nil {
		return nil, err
	}
	fieldID := field.ID
	if supplied, ok := condition["field_type"].(string); ok && supplied != field.Type {
		return nil, fmt.Errorf("过滤条件 field_type 与 Schema 不匹配: %s", fieldID)
	}
	operator, ok := condition["operator"].(string)
	if !ok || !validFilterOperators[operator] {
		return nil, fmt.Errorf("过滤条件 operator 不受支持")
	}
	result := map[string]any{"field_id": fieldID, "field_type": field.Type, "operator": operator}
	valueCount := 0
	for _, key := range []string{"string_value", "number_value", "bool_value", "date_time_value", "user_value"} {
		if value, exists := condition[key]; exists {
			if err := validateFilterValue(key, value); err != nil {
				return nil, err
			}
			result[key] = value
			valueCount++
		}
	}
	if !valueBearingOperator(operator) && valueCount != 0 {
		return nil, fmt.Errorf("操作符 %s 不应携带值", operator)
	}
	if valueBearingOperator(operator) && valueCount != 1 {
		return nil, fmt.Errorf("操作符 %s 必须且只能携带一个值", operator)
	}
	return result, nil
}

func resolveQueryField(fields map[string]config.Field, value map[string]any, prefix string) (config.Field, error) {
	title, hasTitle := value["field_title"].(string)
	id, hasID := value["field_id"].(string)
	hasTitle = hasTitle && strings.TrimSpace(title) != ""
	hasID = hasID && strings.TrimSpace(id) != ""
	if hasTitle == hasID {
		return config.Field{}, fmt.Errorf("%s 必须且只能提供 field_title 或 field_id 之一", prefix)
	}
	if hasTitle {
		field, ok := fields[title]
		if !ok {
			return config.Field{}, fmt.Errorf("%s field_title 未登记: %s", prefix, title)
		}
		return field, nil
	}
	field, ok := fieldByID(fields, id)
	if !ok {
		return config.Field{}, fmt.Errorf("%s field_id 未登记: %s", prefix, id)
	}
	return field, nil
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

func compactQueryResult(response map[string]any, targetRole string, offset, maxBytes int, compact bool, emptyFieldIDs []string) map[string]any {
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
			outputRecords = append(outputRecords, compactQueryRecord(record, emptyFieldIDs))
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

func compactQueryRecord(value any, emptyFieldIDs []string) map[string]any {
	record, _ := value.(map[string]any)
	output := map[string]any{}
	for _, key := range []string{"record_id", "create_time", "update_time"} {
		if record[key] != nil {
			output[key] = record[key]
		}
	}
	values, _ := record["values"].(map[string]any)
	compactValues := map[string]any{}
	keys := make([]string, 0, len(values)+len(emptyFieldIDs))
	for key := range values {
		keys = append(keys, key)
	}
	for _, fieldID := range emptyFieldIDs {
		if _, exists := values[fieldID]; !exists {
			compactValues[fieldID] = nil
			keys = append(keys, fieldID)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value, exists := values[key]; exists {
			compactValues[key] = compactCell(value)
		}
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
