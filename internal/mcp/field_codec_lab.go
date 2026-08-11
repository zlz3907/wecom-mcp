package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

// fieldCodecLabState is intentionally local to one fixed tenant instance. It
// prevents a retried tool call from silently creating multiple test documents.
type fieldCodecLabState struct {
	DocumentID string `json:"document_id"`
	SheetID    string `json:"sheet_id"`
	ShareURL   string `json:"share_url"`
	CreatedAt  string `json:"created_at"`
}

func labStatePath(runtime config.Config) string { return runtime.StatePath + ".field-codec-lab.json" }

const fieldCodecLabRegistryKey = "zhycit_wecom_field_codec_lab_v1"

func (s *Server) fieldCodecLabRegistryStatus(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 登记核对需要白名单允许 %s", operation)
		}
	}
	state, exists := loadFieldCodecLab(labStatePath(runtime))
	if !exists {
		return nil, fmt.Errorf("字段编码验证表本地状态不存在，无法核对登记")
	}
	registrySheetID, fields, records, err := readRegistry(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	registered := false
	conflict := false
	for _, item := range records {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, _ := record["values"].(map[string]any)
		key := cellText(values[fields["registry_key"].ID])
		documentID := cellText(values[fields["docid"].ID])
		if key == fieldCodecLabRegistryKey {
			if documentID == state.DocumentID {
				registered = true
			} else {
				conflict = true
			}
		}
	}
	fieldList := make([]map[string]string, 0, len(fields))
	for _, field := range fields {
		fieldList = append(fieldList, map[string]string{"field_title": field.Title, "field_type": field.Type})
	}
	sort.Slice(fieldList, func(i, j int) bool { return fieldList[i]["field_title"] < fieldList[j]["field_title"] })
	return map[string]any{
		"state": "read", "registry_key": fieldCodecLabRegistryKey,
		"registered": registered, "conflict": conflict, "registry_field_definitions": fieldList,
		"registry_record_count": len(records), "registry_sheet_resolved": registrySheetID != "",
	}, nil
}

func (s *Server) registerFieldCodecLab(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	return s.registerFieldCodecLabInternal(ctx, runtime, client)
}

// registerFieldCodecLabInternal is deliberately part of the create path: a
// validation document is not usable until SMART_SHEETS_IDS contains its
// active, read-back-verified registration.
func (s *Server) registerFieldCodecLabInternal(ctx context.Context, runtime config.Config, client *wecom.Client) (any, error) {
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 登记需要白名单允许 %s", operation)
		}
	}
	state, exists := loadFieldCodecLab(labStatePath(runtime))
	if !exists {
		return nil, fmt.Errorf("字段编码验证表本地状态不存在，拒绝登记")
	}
	registrySheetID, registryFields, registryRecords, err := readRegistry(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	for _, item := range registryRecords {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, _ := record["values"].(map[string]any)
		if cellText(values[registryFields["registry_key"].ID]) != fieldCodecLabRegistryKey {
			continue
		}
		if cellText(values[registryFields["docid"].ID]) == state.DocumentID {
			return map[string]any{"state": "already_registered", "registry_key": fieldCodecLabRegistryKey, "readback_verified": true}, nil
		}
		return nil, fmt.Errorf("SMART_SHEETS_IDS 已存在相同 registry_key 但指向其他文档，拒绝覆盖")
	}

	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(fieldsResponse); err != nil {
		return nil, err
	}
	fingerprint := schemaFingerprint(resultSlice(fieldsResponse, "fields"))
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.RFC3339)
	createdAt := state.CreatedAt
	if parsed, err := time.Parse(time.RFC3339, state.CreatedAt); err == nil {
		createdAt = parsed.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format(time.RFC3339)
	}
	values := map[string]string{
		"registry_key":       fieldCodecLabRegistryKey,
		"name":               "Zoop｜企业微信字段编码验证",
		"type":               "smart_sheet",
		"doc_type":           "10",
		"docid":              state.DocumentID,
		"url":                state.ShareURL,
		"mcp_source":         runtime.TenantRoute,
		"business_domain":    "ai_project_governance",
		"document_role":      "field_codec_lab",
		"lifecycle_status":   "active",
		"schema_version":     "1",
		"schema_fingerprint": fingerprint,
		"created_at":         createdAt,
		"created_by":         "mcp:codex",
		"last_verified_at":   now,
		"last_change_at":     now,
		"last_change_by":     "mcp:codex",
		"last_change_reason": "create and register field codec validation lab",
		"registry_revision":  "1",
		"notes":              "验证企业微信公开字段类型的实际单元格值编码；不承载业务数据。",
	}
	prepared := map[string]any{}
	for title, value := range values {
		field, found := registryFields[title]
		if !found || field.Type != "FIELD_TYPE_TEXT" {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 字段 %s 不存在或不是文本类型，拒绝登记", title)
		}
		prepared[field.ID] = []map[string]string{{"type": "text", "text": value}}
	}
	result, err := client.Request(ctx, "add_records", map[string]any{
		"docid": runtime.RegistryDocumentID, "sheet_id": registrySheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID",
		"records": []map[string]any{{"values": prepared}},
	})
	if err != nil {
		return nil, err
	}
	if err := apiError(result); err != nil {
		return nil, fmt.Errorf("SMART_SHEETS_IDS 登记未确认成功: %w", err)
	}
	_, verifiedFields, verifiedRecords, err := readRegistry(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	for _, item := range verifiedRecords {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row, _ := record["values"].(map[string]any)
		if cellText(row[verifiedFields["registry_key"].ID]) == fieldCodecLabRegistryKey &&
			cellText(row[verifiedFields["docid"].ID]) == state.DocumentID &&
			cellText(row[verifiedFields["lifecycle_status"].ID]) == "active" &&
			cellText(row[verifiedFields["schema_fingerprint"].ID]) == fingerprint {
			return map[string]any{"state": "registered", "registry_key": fieldCodecLabRegistryKey, "readback_verified": true, "schema_fingerprint": fingerprint}, nil
		}
	}
	return nil, fmt.Errorf("SMART_SHEETS_IDS 写入已发起但回读未核验，停止后续使用")
}

func schemaFingerprint(fields []any) string {
	items := make([]map[string]any, 0, len(fields))
	for _, item := range fields {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, field)
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := items[i]["field_id"].(string)
		right, _ := items[j]["field_id"].(string)
		return left < right
	})
	encoded, _ := json.Marshal(items)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum[:])
}

func readRegistry(ctx context.Context, runtime config.Config, client *wecom.Client) (string, map[string]config.Field, []any, error) {
	sheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": runtime.RegistryDocumentID})
	if err != nil {
		return "", nil, nil, err
	}
	if err := apiError(sheets); err != nil {
		return "", nil, nil, err
	}
	sheetID := firstSmartSheetID(sheets)
	if sheetID == "" {
		return "", nil, nil, fmt.Errorf("SMART_SHEETS_IDS 未返回智能子表")
	}
	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": runtime.RegistryDocumentID, "sheet_id": sheetID})
	if err != nil {
		return "", nil, nil, err
	}
	if err := apiError(fieldsResponse); err != nil {
		return "", nil, nil, err
	}
	fields := map[string]config.Field{}
	for _, item := range resultSlice(fieldsResponse, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, titleOK := field["field_title"].(string)
		id, idOK := field["field_id"].(string)
		fieldType, typeOK := field["field_type"].(string)
		if titleOK && idOK && typeOK && title != "" && id != "" && fieldType != "" {
			fields[title] = config.Field{Title: title, ID: id, Type: fieldType}
		}
	}
	for _, required := range []string{"registry_key", "docid", "lifecycle_status"} {
		if fields[required].ID == "" {
			return "", nil, nil, fmt.Errorf("SMART_SHEETS_IDS 缺少必需字段 %s", required)
		}
	}
	recordsResponse, err := client.Request(ctx, "get_records", map[string]any{"docid": runtime.RegistryDocumentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 500})
	if err != nil {
		return "", nil, nil, err
	}
	if err := apiError(recordsResponse); err != nil {
		return "", nil, nil, err
	}
	return sheetID, fields, resultSlice(recordsResponse, "records"), nil
}

func cellText(value any) string {
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

func (s *Server) createFieldCodecLab(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"create_smartsheet", "get_sheet", "add_fields", "add_records", "get_fields", "get_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("字段编码实验台需要白名单允许 %s", operation)
		}
	}
	if existing, ok := loadFieldCodecLab(labStatePath(runtime)); ok {
		repaired, err := repairFieldCodecLab(ctx, runtime, client, existing)
		if err != nil {
			return nil, err
		}
		registered, err := s.registerFieldCodecLabInternal(ctx, runtime, client)
		if err != nil {
			return nil, fmt.Errorf("实验表已存在，但 SMART_SHEETS_IDS 登记未核验；已停止后续使用: %w", err)
		}
		return map[string]any{
			"state": "exists_registered", "docid": existing.DocumentID, "url": existing.ShareURL,
			"document_url": existing.ShareURL, "lab": repaired, "registry": registered,
		}, nil
	}

	created, err := client.Request(ctx, "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "Zoop｜企业微信字段编码验证"})
	if err != nil {
		return nil, err
	}
	if err := apiError(created); err != nil {
		return nil, err
	}
	documentID, shareURL, err := createdDocumentIdentity(created)
	if err != nil {
		return nil, err
	}

	sheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil {
		return nil, err
	}
	if err := apiError(sheets); err != nil {
		return nil, err
	}
	sheetID := firstSmartSheetID(sheets)
	if sheetID == "" {
		return nil, fmt.Errorf("企业微信新建字段编码验证表未返回默认子表")
	}

	base, err := addLabField(ctx, client, documentID, sheetID, map[string]any{"field_title": "样本标识｜文本", "field_type": "FIELD_TYPE_TEXT"})
	if err != nil {
		return nil, fmt.Errorf("创建文本样本列失败: %w", err)
	}
	baseID, _ := base["field_id"].(string)
	if baseID == "" {
		return nil, fmt.Errorf("企业微信未返回文本样本列的字段标识")
	}

	createdTypes := []string{"FIELD_TYPE_TEXT"}
	failed := map[string]string{}
	for _, field := range labFieldDefinitions(baseID) {
		fieldType, _ := field["field_type"].(string)
		if _, err := addLabField(ctx, client, documentID, sheetID, field); err != nil {
			failed[fieldType] = err.Error()
			continue
		}
		createdTypes = append(createdTypes, fieldType)
	}

	// A visible blank row lets the Owner fill values in the WeCom UI without
	// having to create a record first. The text cell encoding is already proven.
	_, sampleError := client.Request(ctx, "add_records", map[string]any{
		"docid": documentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID",
		"records": []map[string]any{{"values": map[string]any{
			baseID: []map[string]string{{"type": "text", "text": "请在此行填写各字段的代表值"}},
		}}},
	})
	if sampleError != nil {
		failed["SAMPLE_RECORD"] = sampleError.Error()
	}

	state := fieldCodecLabState{DocumentID: documentID, SheetID: sheetID, ShareURL: shareURL, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := saveFieldCodecLab(labStatePath(runtime), state); err != nil {
		return nil, err
	}
	registered, err := s.registerFieldCodecLabInternal(ctx, runtime, client)
	if err != nil {
		return nil, fmt.Errorf("实验表已创建，但 SMART_SHEETS_IDS 登记未核验；已停止后续使用，未创建第二张表: %w", err)
	}
	status := "created"
	if len(failed) > 0 {
		status = "created_with_field_errors"
	}
	return map[string]any{
		"state": status, "docid": documentID, "url": shareURL, "document_url": shareURL,
		"field_codec_lab":     "企业微信字段编码验证",
		"created_field_types": createdTypes, "failed_field_types": failed,
		"registry":  registered,
		"next_step": "请在样本行填写各字段的代表值；无需填写创建人、最后编辑人、创建时间、最后编辑时间和自动编号等系统字段。填写后调用 wecom_field_codec_lab_read。",
	}, nil
}

// createdDocumentIdentity preserves the established create_doc contract. The
// client wraps the upstream response under result, but docid and url remain
// unchanged and must never be reconstructed locally.
func createdDocumentIdentity(response map[string]any) (string, string, error) {
	result, _ := response["result"].(map[string]any)
	documentID, _ := result["docid"].(string)
	shareURL, _ := result["url"].(string)
	if documentID == "" || shareURL == "" {
		return "", "", fmt.Errorf("企业微信创建智能表格未返回完整 docid 与 url")
	}
	return documentID, shareURL, nil
}

// repairFieldCodecLab is idempotent: it only adds field types that are absent
// from the fixed lab. It also preserves errors so unsupported property defaults
// become visible evidence instead of being silently hidden.
func repairFieldCodecLab(ctx context.Context, runtime config.Config, client *wecom.Client, state fieldCodecLabState) (any, error) {
	for _, operation := range []string{"get_fields", "add_fields"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("字段编码实验台修复需要白名单允许 %s", operation)
		}
	}
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	sampleID := ""
	for _, item := range resultSlice(response, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fieldType, ok := field["field_type"].(string); ok {
			existing[fieldType] = true
		}
		if field["field_title"] == "样本标识｜文本" {
			sampleID, _ = field["field_id"].(string)
		}
	}
	if sampleID == "" {
		return nil, fmt.Errorf("字段编码验证表缺少样本标识文本列，拒绝修复")
	}
	added := []string{}
	failed := map[string]string{}
	for _, field := range labFieldDefinitions(sampleID) {
		fieldType, _ := field["field_type"].(string)
		if existing[fieldType] {
			continue
		}
		if _, err := addLabField(ctx, client, state.DocumentID, state.SheetID, field); err != nil {
			failed[fieldType] = err.Error()
			continue
		}
		added = append(added, fieldType)
	}
	status := "exists_complete"
	if len(added) > 0 {
		status = "repaired"
	}
	if len(failed) > 0 {
		status = "repair_incomplete"
	}
	return map[string]any{
		"state": status, "docid": state.DocumentID, "url": state.ShareURL, "document_url": state.ShareURL,
		"field_codec_lab":   "企业微信字段编码验证",
		"added_field_types": added, "failed_field_types": failed,
		"next_step": "请在样本行填写各字段的代表值；无需填写系统自动字段。填写后调用 wecom_field_codec_lab_read。",
	}, nil
}

func (s *Server) readFieldCodecLab(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	if !runtime.Allows("get_doc_share_url") || !runtime.Allows("get_doc_auth") || !runtime.Allows("get_sheet") || !runtime.Allows("get_fields") || !runtime.Allows("get_records") {
		return nil, fmt.Errorf("字段编码实验台读取需要白名单允许 get_doc_share_url、get_doc_auth、get_sheet、get_fields 和 get_records")
	}
	state, ok := loadFieldCodecLab(labStatePath(runtime))
	if !ok {
		return nil, fmt.Errorf("字段编码验证表尚未创建，请先调用 wecom_field_codec_lab_create")
	}
	registration, err := s.fieldCodecLabRegistryStatus(ctx, runtime, client, json.RawMessage("{}"))
	if err != nil {
		return nil, err
	}
	registryStatus, _ := registration.(map[string]any)
	if registered, _ := registryStatus["registered"].(bool); !registered {
		return nil, fmt.Errorf("字段编码验证表尚未在 SMART_SHEETS_IDS 处于已登记状态，停止读取")
	}
	share, err := client.Request(ctx, "get_doc_share_url", map[string]any{"docid": state.DocumentID})
	if err != nil {
		return nil, err
	}
	if err := apiError(share); err != nil {
		return nil, err
	}
	shareResult, _ := share["result"].(map[string]any)
	shareURL, _ := shareResult["share_url"].(string)
	if shareURL == "" {
		return nil, fmt.Errorf("企业微信未返回字段编码验证表的访问 URL")
	}
	if shareURL != state.ShareURL {
		state.ShareURL = shareURL
		if err := saveFieldCodecLab(labStatePath(runtime), state); err != nil {
			return nil, err
		}
	}
	authorization, err := client.Request(ctx, "get_doc_auth", map[string]any{"docid": state.DocumentID})
	if err != nil {
		return nil, err
	}
	if err := apiError(authorization); err != nil {
		return nil, err
	}
	fields, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(fields); err != nil {
		return nil, err
	}
	records, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 50})
	if err != nil {
		return nil, err
	}
	if err := apiError(records); err != nil {
		return nil, err
	}
	return map[string]any{
		"state": "read", "docid": state.DocumentID, "url": shareURL, "document_url": shareURL,
		"field_codec_lab": "企业微信字段编码验证",
		"fields":          resultSlice(fields, "fields"), "records": resultSlice(records, "records"),
		"registry":      registration,
		"authorization": summarizeDocumentAuthorization(authorization),
		"purpose":       "这些原始字段和值只用于确认企业微信实际回读形状，随后再固化为 MCP 写入 codec。",
	}, nil
}

func (s *Server) debugFieldCodecLabReference(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("关联字段对照读取需要白名单允许 %s", operation)
		}
	}
	state, ok := loadFieldCodecLab(labStatePath(runtime))
	if !ok {
		return nil, fmt.Errorf("字段编码验证表尚未创建")
	}
	fields, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(fields); err != nil {
		return nil, err
	}
	referenceID, markerID := "", ""
	for _, item := range resultSlice(fields, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := field["field_title"].(string)
		id, _ := field["field_id"].(string)
		if title == "关联" {
			referenceID = id
		}
		if title == "样本标识｜文本" {
			markerID = id
		}
	}
	if referenceID == "" {
		return nil, fmt.Errorf("验证表缺少关联字段")
	}
	byID, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(byID); err != nil {
		return nil, err
	}
	defaultKey, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(defaultKey); err != nil {
		return nil, err
	}
	summarize := func(records []any) []map[string]any {
		output := []map[string]any{}
		for _, item := range records {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			values, _ := row["values"].(map[string]any)
			marker := values[markerID]
			if marker == nil {
				marker = values["样本标识｜文本"]
			}
			reference := values[referenceID]
			if reference == nil {
				reference = values["关联"]
			}
			output = append(output, map[string]any{"record_id": row["record_id"], "marker": cellText(marker), "reference": reference})
		}
		return output
	}
	return map[string]any{"state": "read", "reference_field_id": referenceID, "with_field_id_key": summarize(resultSlice(byID, "records")), "without_key_type": summarize(resultSlice(defaultKey, "records"))}, nil
}

// writeFieldCodecLabReferenceProbe is deliberately isolated from user-filled
// rows. It proves the documented write form without treating an incomplete
// get_records response as a successful readback.
func (s *Server) writeFieldCodecLabReferenceProbe(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_records", "update_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("关联字段写入探针需要白名单允许 %s", operation)
		}
	}
	state, ok := loadFieldCodecLab(labStatePath(runtime))
	if !ok {
		return nil, fmt.Errorf("字段编码验证表尚未创建")
	}
	registration, err := s.fieldCodecLabRegistryStatus(ctx, runtime, client, json.RawMessage("{}"))
	if err != nil {
		return nil, err
	}
	if registered, _ := registration.(map[string]any)["registered"].(bool); !registered {
		return nil, fmt.Errorf("字段编码验证表未登记，停止写入")
	}
	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(fieldsResponse); err != nil {
		return nil, err
	}
	markerID, referenceID, referenceTargetSheetID := "", "", ""
	for _, item := range resultSlice(fieldsResponse, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fieldID, _ := field["field_id"].(string)
		if field["field_title"] == "样本标识｜文本" && field["field_type"] == "FIELD_TYPE_TEXT" {
			markerID = fieldID
		}
		if field["field_type"] == "FIELD_TYPE_REFERENCE" {
			referenceID = fieldID
			referenceTargetSheetID, _ = referenceProperty(field)["sub_id"].(string)
		}
	}
	if markerID == "" || referenceID == "" || referenceTargetSheetID == "" {
		return nil, fmt.Errorf("验证表缺少文本标识或关联字段")
	}
	if referenceTargetSheetID == state.SheetID {
		return nil, fmt.Errorf("关联字段写入探针只验证跨表关联，目标子表不能是来源子表")
	}
	targetFieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": referenceTargetSheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(targetFieldsResponse); err != nil {
		return nil, err
	}
	targetMarkerID := ""
	for _, item := range resultSlice(targetFieldsResponse, "fields") {
		field, ok := item.(map[string]any)
		if !ok || field["field_type"] != "FIELD_TYPE_TEXT" {
			continue
		}
		targetMarkerID, _ = field["field_id"].(string)
		if targetMarkerID != "" {
			break
		}
	}
	if targetMarkerID == "" {
		return nil, fmt.Errorf("关联目标子表缺少可写文本字段")
	}
	targetMarker := "MCP 关联写入探针｜目标"
	targetWrite, err := client.Request(ctx, "add_records", map[string]any{
		"docid": state.DocumentID, "sheet_id": referenceTargetSheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID",
		"records": []map[string]any{{"values": map[string]any{targetMarkerID: textCell(targetMarker)}}},
	})
	if err != nil {
		return nil, err
	}
	if err := apiError(targetWrite); err != nil {
		return nil, err
	}
	targetID, err := exactlyOneWrittenRecordID(targetWrite, "关联目标")
	if err != nil {
		return nil, err
	}
	sourceMarker := "MCP 关联写入探针｜新增"
	sourceWrite, err := client.Request(ctx, "add_records", map[string]any{
		"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID",
		"records": []map[string]any{{"values": map[string]any{
			markerID: textCell(sourceMarker), referenceID: referenceCell(targetID),
		}}},
	})
	if err != nil {
		return nil, err
	}
	if err := apiError(sourceWrite); err != nil {
		return nil, err
	}
	sourceID, err := exactlyOneWrittenRecordID(sourceWrite, "关联来源")
	if err != nil {
		return nil, err
	}
	updatedMarker := "MCP 关联写入探针｜已更新"
	updated, err := client.Request(ctx, "update_records", map[string]any{
		"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID",
		"records": []map[string]any{{"record_id": sourceID, "values": map[string]any{
			markerID: textCell(updatedMarker), referenceID: referenceCell(targetID),
		}}},
	})
	if err != nil {
		return nil, err
	}
	if err := apiError(updated); err != nil {
		return nil, err
	}
	readback, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(readback); err != nil {
		return nil, err
	}
	for _, item := range resultSlice(readback, "records") {
		row, ok := item.(map[string]any)
		if !ok || row["record_id"] != sourceID {
			continue
		}
		values, _ := row["values"].(map[string]any)
		if cellText(values[markerID]) != updatedMarker {
			return nil, fmt.Errorf("关联来源记录更新后未回读到标识字段")
		}
		actualReference := values[referenceID]
		if actualReference == nil {
			return map[string]any{"state": "write_accepted_readback_missing_reference", "target_record_id": targetID, "source_record_id": sourceID, "reference_value": nil}, nil
		}
		if canonicalValue(actualReference) == canonicalValue(referenceCell(targetID)) {
			return map[string]any{"state": "inserted_updated_readback_verified", "verified_field_types": []string{"FIELD_TYPE_REFERENCE"}, "target_record_id": targetID, "source_record_id": sourceID}, nil
		}
		return map[string]any{"state": "write_accepted_readback_inconclusive", "target_record_id": targetID, "source_record_id": sourceID, "reference_value": actualReference}, nil
	}
	return nil, fmt.Errorf("关联来源记录写入后未回读")
}

func textCell(value string) []map[string]string {
	return []map[string]string{{"type": "text", "text": value}}
}

func referenceCell(recordID string) []string {
	return []string{recordID}
}

func exactlyOneWrittenRecordID(response map[string]any, label string) (string, error) {
	rows := resultSlice(response, "records")
	if len(rows) != 1 {
		return "", fmt.Errorf("%s写入未返回唯一记录", label)
	}
	row, _ := rows[0].(map[string]any)
	recordID, _ := row["record_id"].(string)
	if recordID == "" {
		return "", fmt.Errorf("%s写入未返回 record_id", label)
	}
	return recordID, nil
}

// writeFieldCodecLabProbe verifies only codecs already evidenced by the
// contract: text cells and boolean checkbox values. It intentionally does not
// invent values for the remaining field types.
func (s *Server) writeFieldCodecLabProbe(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("字段编码探针需要白名单允许 %s", operation)
		}
	}
	state, ok := loadFieldCodecLab(labStatePath(runtime))
	if !ok {
		return nil, fmt.Errorf("字段编码验证表尚未创建")
	}
	registration, err := s.fieldCodecLabRegistryStatus(ctx, runtime, client, json.RawMessage("{}"))
	if err != nil {
		return nil, err
	}
	status, _ := registration.(map[string]any)
	if registered, _ := status["registered"].(bool); !registered {
		return nil, fmt.Errorf("字段编码验证表未登记，停止写入")
	}
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	fieldID := map[string]string{}
	for _, item := range resultSlice(response, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := field["field_title"].(string)
		id, _ := field["field_id"].(string)
		fieldType, _ := field["field_type"].(string)
		if title == "样本标识｜文本" && fieldType == "FIELD_TYPE_TEXT" || title == "复选框" && fieldType == "FIELD_TYPE_CHECKBOX" {
			fieldID[title] = id
		}
	}
	if fieldID["样本标识｜文本"] == "" || fieldID["复选框"] == "" {
		return nil, fmt.Errorf("验证表缺少文本或复选框探针字段")
	}
	marker := "MCP 写入探针｜文本+复选框"
	written, err := client.Request(ctx, "add_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []map[string]any{{"values": map[string]any{
		fieldID["样本标识｜文本"]: []map[string]string{{"type": "text", "text": marker}}, fieldID["复选框"]: true,
	}}}})
	if err != nil {
		return nil, err
	}
	if err := apiError(written); err != nil {
		return nil, err
	}
	created := resultSlice(written, "records")
	if len(created) != 1 {
		return nil, fmt.Errorf("探针写入未返回唯一记录")
	}
	createdRow, _ := created[0].(map[string]any)
	recordID, _ := createdRow["record_id"].(string)
	if recordID == "" {
		return nil, fmt.Errorf("探针写入未返回 record_id")
	}
	readback, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(readback); err != nil {
		return nil, err
	}
	for _, item := range resultSlice(readback, "records") {
		row, ok := item.(map[string]any)
		if !ok || row["record_id"] != recordID {
			continue
		}
		values, _ := row["values"].(map[string]any)
		if cellText(values[fieldID["样本标识｜文本"]]) == marker {
			if checked, ok := values[fieldID["复选框"]].(bool); ok && checked {
				return map[string]any{"state": "applied_readback_verified", "verified_field_types": []string{"FIELD_TYPE_TEXT", "FIELD_TYPE_CHECKBOX"}, "record_id": recordID}, nil
			}
		}
	}
	return nil, fmt.Errorf("探针已发起写入，但文本和复选框回读未同时核验")
}

func (s *Server) replayFieldCodecLabProbe(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_records", "update_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("字段回放探针需要白名单允许 %s", operation)
		}
	}
	state, ok := loadFieldCodecLab(labStatePath(runtime))
	if !ok {
		return nil, fmt.Errorf("字段编码验证表尚未创建")
	}
	registration, err := s.fieldCodecLabRegistryStatus(ctx, runtime, client, json.RawMessage("{}"))
	if err != nil {
		return nil, err
	}
	if registered, _ := registration.(map[string]any)["registered"].(bool); !registered {
		return nil, fmt.Errorf("字段编码验证表未登记，停止写入")
	}
	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(fieldsResponse); err != nil {
		return nil, err
	}
	fieldTypes, markerID, checkboxID := map[string]string{}, "", ""
	for _, item := range resultSlice(fieldsResponse, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := field["field_id"].(string)
		title, _ := field["field_title"].(string)
		fieldTypes[id], _ = field["field_type"].(string)
		if title == "样本标识｜文本" {
			markerID = id
		}
		if title == "复选框" {
			checkboxID = id
		}
	}
	if markerID == "" || checkboxID == "" {
		return nil, fmt.Errorf("验证表缺少探针字段")
	}
	recordsResponse, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(recordsResponse); err != nil {
		return nil, err
	}
	var source map[string]any
	for _, item := range resultSlice(recordsResponse, "records") {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, _ := row["values"].(map[string]any)
		if strings.HasPrefix(cellText(values[markerID]), "MCP 写入探针") {
			source = values
			break
		}
	}
	if source == nil {
		return nil, fmt.Errorf("未找到已人工填写的字段样本记录")
	}
	readOnly := map[string]bool{"FIELD_TYPE_CREATED_USER": true, "FIELD_TYPE_MODIFIED_USER": true, "FIELD_TYPE_CREATED_TIME": true, "FIELD_TYPE_MODIFIED_TIME": true, "FIELD_TYPE_AUTONUMBER": true, "FIELD_TYPE_REFERENCE": true}
	values := map[string]any{}
	for fieldID, value := range source {
		if !readOnly[fieldTypes[fieldID]] && fieldID != markerID {
			values[fieldID] = value
		}
	}
	values[markerID] = []map[string]string{{"type": "text", "text": "MCP 全字段回放｜新增"}}
	created, err := client.Request(ctx, "add_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []map[string]any{{"values": values}}})
	if err != nil {
		return nil, err
	}
	if err := apiError(created); err != nil {
		return nil, err
	}
	createdRows := resultSlice(created, "records")
	if len(createdRows) != 1 {
		return nil, fmt.Errorf("新增回放记录未返回唯一 record_id")
	}
	createdRow, _ := createdRows[0].(map[string]any)
	recordID, _ := createdRow["record_id"].(string)
	if recordID == "" {
		return nil, fmt.Errorf("新增回放记录未返回 record_id")
	}
	updatedMarker := "MCP 全字段回放｜已更新"
	updated, err := client.Request(ctx, "update_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []map[string]any{{"record_id": recordID, "values": map[string]any{markerID: []map[string]string{{"type": "text", "text": updatedMarker}}, checkboxID: false}}}})
	if err != nil {
		return nil, err
	}
	if err := apiError(updated); err != nil {
		return nil, err
	}
	verified, err := client.Request(ctx, "get_records", map[string]any{"docid": state.DocumentID, "sheet_id": state.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 100})
	if err != nil {
		return nil, err
	}
	if err := apiError(verified); err != nil {
		return nil, err
	}
	for _, item := range resultSlice(verified, "records") {
		row, ok := item.(map[string]any)
		if !ok || row["record_id"] != recordID {
			continue
		}
		result, _ := row["values"].(map[string]any)
		if cellText(result[markerID]) == updatedMarker {
			if checked, ok := result[checkboxID].(bool); ok && !checked {
				types := make([]string, 0, len(values))
				for id := range values {
					types = append(types, fieldTypes[id])
				}
				sort.Strings(types)
				return map[string]any{"state": "inserted_updated_readback_verified", "record_id": recordID, "inserted_field_types": types}, nil
			}
		}
	}
	return nil, fmt.Errorf("回放记录已写入，但更新回读未核验")
}

func summarizeDocumentAuthorization(response map[string]any) map[string]any {
	result, _ := response["result"].(map[string]any)
	keys := make([]string, 0, len(result))
	for key := range result {
		if key != "errcode" && key != "errmsg" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	summary := map[string]any{"readable": true, "returned_keys": keys}
	for _, key := range []string{"access_rule", "secure_setting"} {
		if value, exists := result[key]; exists {
			summary[key] = value
		}
	}
	for _, key := range []string{"join_rule", "doc_join_rule", "auth_type"} {
		if value, exists := result[key]; exists {
			summary[key] = value
		}
	}
	for _, key := range []string{"member_list", "doc_member_list", "file_member_list"} {
		if list, ok := result[key].([]any); ok {
			summary["member_count"] = len(list)
			break
		}
	}
	return summary
}

func addLabField(ctx context.Context, client *wecom.Client, documentID, sheetID string, field map[string]any) (map[string]any, error) {
	response, err := client.Request(ctx, "add_fields", map[string]any{"docid": documentID, "sheet_id": sheetID, "fields": []map[string]any{field}})
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	items := resultSlice(response, "fields")
	if len(items) != 1 {
		return nil, fmt.Errorf("企业微信添加字段未返回唯一字段定义")
	}
	fieldResult, ok := items[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("企业微信添加字段返回格式无效")
	}
	return fieldResult, nil
}

func firstSmartSheetID(response map[string]any) string {
	for _, item := range resultSlice(response, "sheet_list") {
		sheet, ok := item.(map[string]any)
		if !ok || sheet["type"] != "smartsheet" {
			continue
		}
		if id, ok := sheet["sheet_id"].(string); ok {
			return id
		}
	}
	return ""
}

func resultSlice(response map[string]any, key string) []any {
	result, _ := response["result"].(map[string]any)
	items, _ := result[key].([]any)
	return items
}

func labFieldDefinitions(sampleFieldID string) []map[string]any {
	return []map[string]any{
		{"field_title": "数字", "field_type": "FIELD_TYPE_NUMBER", "property_number": map[string]any{}},
		{"field_title": "复选框", "field_type": "FIELD_TYPE_CHECKBOX", "property_checkbox": map[string]any{"checked": false}},
		{"field_title": "日期", "field_type": "FIELD_TYPE_DATE_TIME", "property_date_time": map[string]any{}},
		{"field_title": "图片", "field_type": "FIELD_TYPE_IMAGE"},
		{"field_title": "文件", "field_type": "FIELD_TYPE_ATTACHMENT"},
		{"field_title": "成员", "field_type": "FIELD_TYPE_USER"},
		{"field_title": "超链接", "field_type": "FIELD_TYPE_URL", "property_url": map[string]any{"type": "LINK_TYPE_PURE_TEXT"}},
		{"field_title": "多选", "field_type": "FIELD_TYPE_SELECT", "property_select": map[string]any{"is_quick_add": true, "options": []any{}}},
		{"field_title": "创建人", "field_type": "FIELD_TYPE_CREATED_USER"},
		{"field_title": "最后编辑人", "field_type": "FIELD_TYPE_MODIFIED_USER"},
		{"field_title": "创建时间", "field_type": "FIELD_TYPE_CREATED_TIME", "property_created_time": map[string]any{"format": "yyyy-mm-dd hh:mm"}},
		{"field_title": "最后编辑时间", "field_type": "FIELD_TYPE_MODIFIED_TIME", "property_modified_time": map[string]any{"format": "yyyy-mm-dd hh:mm"}},
		{"field_title": "进度", "field_type": "FIELD_TYPE_PROGRESS", "property_progress": map[string]any{}},
		{"field_title": "电话", "field_type": "FIELD_TYPE_PHONE_NUMBER"},
		{"field_title": "邮箱", "field_type": "FIELD_TYPE_EMAIL"},
		{"field_title": "单选", "field_type": "FIELD_TYPE_SINGLE_SELECT", "property_single_select": map[string]any{"is_quick_add": true, "options": []any{}}},
		{"field_title": "关联", "field_type": "FIELD_TYPE_REFERENCE", "property_reference": map[string]any{"sub_id": "", "field_id": sampleFieldID, "is_multiple": false}},
		{"field_title": "地理位置", "field_type": "FIELD_TYPE_LOCATION", "property_location": map[string]any{"input_type": "LOCATION_INPUT_TYPE_MANUAL"}},
		{"field_title": "货币", "field_type": "FIELD_TYPE_CURRENCY", "property_currency": map[string]any{"currency_type": "CURRENCY_TYPE_CNY", "decimal_places": 2, "use_separate": false}},
		{"field_title": "群", "field_type": "FIELD_TYPE_WWGROUP"},
		{"field_title": "自动编号", "field_type": "FIELD_TYPE_AUTONUMBER", "property_auto_number": map[string]any{"type": "NUMBER_TYPE_INCR", "rules": []any{}, "reformat_existing_record": false}},
		{"field_title": "百分数", "field_type": "FIELD_TYPE_PERCENTAGE", "property_percentage": map[string]any{"decimal_places": 2, "use_separate": false}},
		{"field_title": "条码", "field_type": "FIELD_TYPE_BARCODE"},
	}
}

func loadFieldCodecLab(path string) (fieldCodecLabState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fieldCodecLabState{}, false
	}
	var state fieldCodecLabState
	if json.Unmarshal(data, &state) != nil || state.DocumentID == "" || state.SheetID == "" {
		return fieldCodecLabState{}, false
	}
	return state, true
}

func saveFieldCodecLab(path string, state fieldCodecLabState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建字段编码实验台本地状态目录失败: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
