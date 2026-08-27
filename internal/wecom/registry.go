package wecom

import (
	"context"
	"fmt"
	"strings"
)

type Target struct{ DocumentID, SheetID, Role string }

type Requester interface {
	Request(context.Context, string, any) (map[string]any, error)
}

func textCell(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := object["text"].(string); ok {
			return text
		}
	}
	return ""
}

func resultMap(response map[string]any) map[string]any {
	value, _ := response["result"].(map[string]any)
	return value
}

func responseError(response map[string]any) error {
	result := resultMap(response)
	code, exists := result["errcode"]
	if !exists {
		return nil
	}
	value, numeric := code.(float64)
	if numeric && value != 0 {
		message, _ := result["errmsg"].(string)
		return fmt.Errorf("企业微信 API 返回 errcode %.0f: %s", value, message)
	}
	return nil
}

// ResolveTarget discovers the active document through the instance's fixed
// SMART_SHEETS_IDS entrypoint, then resolves one logical Zoop role. It never
// accepts a caller-provided document or sheet ID.
func ResolveTarget(ctx context.Context, client Requester, registryDocumentID, registryKey, role string, allowed func(string) bool) (Target, error) {
	for _, op := range []string{"get_sheet", "get_fields", "get_records"} {
		if !allowed(op) {
			return Target{}, fmt.Errorf("实例白名单未允许登记解析所需 API: %s", op)
		}
	}
	registrySheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": registryDocumentID})
	if err != nil {
		return Target{}, err
	}
	if err := responseError(registrySheets); err != nil {
		return Target{}, err
	}
	list, _ := resultMap(registrySheets)["sheet_list"].([]any)
	registrySheetID := ""
	for _, item := range list {
		sheet, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if sheet["type"] == "smartsheet" {
			registrySheetID, _ = sheet["sheet_id"].(string)
			break
		}
	}
	if registrySheetID == "" {
		return Target{}, fmt.Errorf("SMART_SHEETS_IDS 未找到智能表（返回子表数：%d）", len(list))
	}
	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": registryDocumentID, "sheet_id": registrySheetID})
	if err != nil {
		return Target{}, err
	}
	if err := responseError(fieldsResponse); err != nil {
		return Target{}, err
	}
	fieldIDs := map[string]string{}
	fields, _ := resultMap(fieldsResponse)["fields"].([]any)
	for _, item := range fields {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, titleOK := field["field_title"].(string)
		id, idOK := field["field_id"].(string)
		if titleOK && idOK {
			fieldIDs[title] = id
		}
	}
	for _, title := range []string{"registry_key", "docid", "lifecycle_status"} {
		if fieldIDs[title] == "" {
			return Target{}, fmt.Errorf("SMART_SHEETS_IDS 缺少字段 %s", title)
		}
	}
	recordsResponse, err := client.Request(ctx, "get_records", map[string]any{"docid": registryDocumentID, "sheet_id": registrySheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 500})
	if err != nil {
		return Target{}, err
	}
	if err := responseError(recordsResponse); err != nil {
		return Target{}, err
	}
	records, _ := resultMap(recordsResponse)["records"].([]any)
	targetDocumentID := ""
	matches := 0
	for _, item := range records {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, _ := record["values"].(map[string]any)
		if textCell(values[fieldIDs["registry_key"]]) != registryKey || textCell(values[fieldIDs["lifecycle_status"]]) != "active" {
			continue
		}
		matches++
		targetDocumentID = textCell(values[fieldIDs["docid"]])
	}
	if matches != 1 || targetDocumentID == "" {
		return Target{}, fmt.Errorf("受管 Zoop 登记项未找到、冲突或未 active")
	}
	targetSheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": targetDocumentID})
	if err != nil {
		return Target{}, err
	}
	if err := responseError(targetSheets); err != nil {
		return Target{}, err
	}
	targetList, _ := resultMap(targetSheets)["sheet_list"].([]any)
	targets, err := targetsFromSheetList(targetDocumentID, []string{role}, targetList)
	if err != nil {
		return Target{}, err
	}
	return targets[role], nil
}

// ResolveTargets resolves several logical Zoop roles after discovering the
// fixed active document once. It avoids repeating the SMART_SHEETS_IDS lookup
// for every role during an Owner-authorized full Schema sync.
func ResolveTargets(ctx context.Context, client Requester, registryDocumentID, registryKey string, roles []string, allowed func(string) bool) (map[string]Target, error) {
	if len(roles) == 0 {
		return nil, fmt.Errorf("至少需要一个 Zoop 表角色")
	}
	first, err := ResolveTarget(ctx, client, registryDocumentID, registryKey, roles[0], allowed)
	if err != nil {
		return nil, err
	}
	if len(roles) == 1 {
		return map[string]Target{roles[0]: first}, nil
	}
	targetSheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": first.DocumentID})
	if err != nil {
		return nil, err
	}
	if err := responseError(targetSheets); err != nil {
		return nil, err
	}
	targetList, _ := resultMap(targetSheets)["sheet_list"].([]any)
	return targetsFromSheetList(first.DocumentID, roles, targetList)
}

func targetsFromSheetList(documentID string, roles []string, targetList []any) (map[string]Target, error) {
	availableNames := make([]string, 0, len(targetList))
	for _, item := range targetList {
		sheet, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, nameOK := sheetName(sheet)
		if nameOK {
			availableNames = append(availableNames, name)
		}
	}
	targets := make(map[string]Target, len(roles))
	for _, role := range roles {
		prefix := role + "｜"
		sheetID := ""
		matches := 0
		for _, item := range targetList {
			sheet, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, nameOK := sheetName(sheet)
			id, idOK := sheet["sheet_id"].(string)
			if nameOK && idOK && strings.HasPrefix(name, prefix) {
				matches++
				sheetID = id
			}
		}
		if matches != 1 || sheetID == "" {
			return nil, fmt.Errorf("Zoop %s 子表未找到或不唯一（可见子表：%s）", role, strings.Join(availableNames, "、"))
		}
		targets[role] = Target{DocumentID: documentID, SheetID: sheetID, Role: role}
	}
	return targets, nil
}

// ReadFields returns the raw field definitions for a previously resolved
// target. Callers may use it for a read-only probe or an explicit
// Owner-authorized local mirror sync; this helper never persists the result.
func ReadFields(ctx context.Context, client *Client, target Target, allowed func(string) bool) ([]map[string]any, error) {
	if !allowed("get_fields") {
		return nil, fmt.Errorf("实例白名单未允许 get_fields")
	}
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": target.DocumentID, "sheet_id": target.SheetID})
	if err != nil {
		return nil, err
	}
	if err := responseError(response); err != nil {
		return nil, err
	}
	items, _ := resultMap(response)["fields"].([]any)
	fields := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if field, ok := item.(map[string]any); ok {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%s 未返回字段定义", target.Role)
	}
	return fields, nil
}

func sheetName(sheet map[string]any) (string, bool) {
	for _, key := range []string{"name", "sheet_name", "title"} {
		if value, ok := sheet[key].(string); ok && value != "" {
			return value, true
		}
	}
	return "", false
}
