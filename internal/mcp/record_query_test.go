package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func queryTestFields() map[string]config.Field {
	return map[string]config.Field{
		"任务编号": {Title: "任务编号", ID: "field_task_id", Type: "FIELD_TYPE_TEXT"},
		"任务状态": {Title: "任务状态", ID: "fGyxtt", Type: "FIELD_TYPE_SINGLE_SELECT"},
	}
}

func TestNormalizeQuerySortUsesSchemaTitleAndRejectsUnknownField(t *testing.T) {
	rules, err := normalizeQuerySort(queryTestFields(), []map[string]any{{"field_id": "field_task_id", "desc": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rules[0]["field_title"]; got != "任务编号" {
		t.Fatalf("sort title=%v", got)
	}
	if _, err := normalizeQuerySort(queryTestFields(), []map[string]any{{"field_id": "not-a-field"}}); err == nil {
		t.Fatal("unknown sort field must be rejected")
	}
	titleRules, err := normalizeQuerySort(queryTestFields(), []map[string]any{{"field_title": "任务编号"}})
	if err != nil || titleRules[0]["field_title"] != "任务编号" {
		t.Fatalf("title sort was not resolved: %#v err=%v", titleRules, err)
	}
}

func TestFilterSpecIsNormalizedAgainstSchema(t *testing.T) {
	got, err := validateAndNormalizeFilter(queryTestFields(), map[string]any{
		"conjunction": "CONJUNCTION_AND",
		"conditions": []any{map[string]any{
			"field_id": "field_task_id", "operator": "OPERATOR_IS",
			"string_value": map[string]any{"value": []any{"TASK-EXAMPLE-001"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	condition := got["conditions"].([]any)[0].(map[string]any)
	if condition["field_type"] != "FIELD_TYPE_TEXT" {
		t.Fatalf("field type was not filled from schema: %#v", condition)
	}
	byTitle, err := validateAndNormalizeFilter(queryTestFields(), map[string]any{
		"conjunction": "CONJUNCTION_AND",
		"conditions": []any{map[string]any{
			"field_title": "任务编号", "operator": "OPERATOR_IS",
			"string_value": map[string]any{"value": []any{"TASK-EXAMPLE-001"}},
		}},
	})
	if err != nil || byTitle["conditions"].([]any)[0].(map[string]any)["field_id"] != "field_task_id" {
		t.Fatalf("title filter was not resolved: %#v err=%v", byTitle, err)
	}
	if _, err := validateAndNormalizeFilter(queryTestFields(), map[string]any{
		"conjunction": "CONJUNCTION_AND",
		"conditions": []any{map[string]any{
			"field_title": "任务编号", "field_id": "field_task_id", "operator": "OPERATOR_IS",
			"string_value": map[string]any{"value": []any{"TASK-EXAMPLE-001"}},
		}},
	}); err == nil {
		t.Fatal("filter must reject simultaneous field_title and field_id")
	}
	if _, err := validateAndNormalizeFilter(queryTestFields(), map[string]any{
		"conjunction": "CONJUNCTION_AND",
		"conditions": []any{map[string]any{
			"field_title": "任务编号", "operator": "OPERATOR_IS",
			"string_value": map[string]any{"value": []any{"TASK-EXAMPLE-001"}},
			"bool_value":   map[string]any{"value": true},
		}},
	}); err == nil {
		t.Fatal("filter must reject more than one value object")
	}
	if _, err := validateAndNormalizeFilter(queryTestFields(), map[string]any{
		"conjunction": "CONJUNCTION_AND",
		"conditions": []any{map[string]any{
			"field_id": "field_task_id", "field_type": "FIELD_TYPE_NUMBER", "operator": "OPERATOR_IS",
			"string_value": map[string]any{"value": []any{"wrong type"}},
		}},
	}); err == nil {
		t.Fatal("filter field type mismatch must be rejected")
	}
}

func TestResolveQueryProjectionSupportsTitles(t *testing.T) {
	got, err := resolveQueryProjection(queryTestFields(), nil, []string{"任务状态", "任务编号"})
	if err != nil || len(got) != 2 || got[0] != "fGyxtt" || got[1] != "field_task_id" {
		t.Fatalf("title projection=%#v err=%v", got, err)
	}
	if _, err := resolveQueryProjection(queryTestFields(), []string{"field_task_id"}, []string{"任务编号"}); err == nil {
		t.Fatal("projection must reject IDs and titles together")
	}
}

func TestRecordQueryToolSchemaPublishesNestedContract(t *testing.T) {
	properties := recordQueryToolSchema()["properties"].(map[string]any)
	filter := properties["filter_spec"].(map[string]any)
	filterProperties := filter["properties"].(map[string]any)
	conditions := filterProperties["conditions"].(map[string]any)
	condition := conditions["items"].(map[string]any)
	conditionProperties := condition["properties"].(map[string]any)
	for _, key := range []string{"field_title", "field_id", "operator", "string_value", "number_value", "bool_value", "date_time_value", "user_value"} {
		if conditionProperties[key] == nil {
			t.Fatalf("filter schema omitted %s", key)
		}
	}
	operator := conditionProperties["operator"].(map[string]any)
	if len(operator["enum"].([]string)) != len(validFilterOperators) {
		t.Fatalf("operator schema drift: %#v", operator)
	}
}

func TestCompactQueryResultReportsResponseTruncation(t *testing.T) {
	response := map[string]any{"result": map[string]any{
		"total": float64(2), "has_more": false, "records": []any{
			map[string]any{"record_id": "r1", "values": map[string]any{"field": []any{map[string]any{"text": "one"}}}},
			map[string]any{"record_id": "r2", "values": map[string]any{"field": []any{map[string]any{"text": "two"}}}},
		},
	}}
	got := compactQueryResult(response, "Z-S03", 0, 190, true)
	if got["response_truncated"] != true || got["has_more"] != true || got["next_offset"] != 1 {
		t.Fatalf("truncation metadata=%#v", got)
	}
	if got["returned_count"] != 1 {
		t.Fatalf("returned_count=%v", got["returned_count"])
	}
	if value := got["records"].([]any)[0].(map[string]any)["values"].(map[string]any)["field"].([]any)[0]; value != "one" {
		t.Fatalf("compact cell=%v", value)
	}
}

func TestRecordQueryToolSchemaDoesNotExposeRouting(t *testing.T) {
	data, err := json.Marshal(recordQueryToolSchema())
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	for _, forbidden := range []string{"docid", "sheet_id", "tenant", "access_token"} {
		if containsJSONKey(encoded, forbidden) {
			t.Fatalf("tool schema exposes forbidden routing key %q: %s", forbidden, encoded)
		}
	}
}

func containsJSONKey(encoded, key string) bool {
	return strings.Contains(encoded, `"`+key+`"`)
}
