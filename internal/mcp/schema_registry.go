package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

const (
	schemaRegistryGroup         = "schema_registry"
	schemaRegistryAuthorization = "refresh_online_schema_registry"
	schemaRegistryActiveKey     = "@active"
	schemaRegistryBatchSize     = 50
)

var schemaRegistryDigestFields = []string{
	"条目类型", "实例名称", "Registry Key", "表角色", "表名", "表 ID", "字段名", "字段 ID", "字段类型",
	"是否主字段", "是否多值", "选项定义", "关联目标表 ID", "关联目标字段 ID", "原始字段属性",
	"是否系统字段", "是否允许新增", "是否允许更新", "写入编码器", "Codec 验证状态",
}

type schemaRegistrySnapshot struct {
	Generation string
	CapturedAt string
	FieldCount int
	Entries    []map[string]string
}

type schemaRegistryTable struct {
	Target           wecom.Target
	FieldIDs         map[string]string
	Records          []any
	ByKey            map[string]map[string]any
	ActiveGeneration string
	ActiveRecordID   string
	ActiveComplete   bool
	ActiveFieldCount int
}

func schemaRegistryStatusToolSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false}
}

func schemaRegistryUpdateToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"idempotency_key", "source_revision", "expected_active_generation", "owner_authorization"},
		"properties": map[string]any{
			"idempotency_key": map[string]any{
				"type": "string", "minLength": 16, "maxLength": 256,
				"description": "本次 Registry generation 更新的幂等键；结果不确定时禁止更换键盲目重写。",
			},
			"source_revision": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 256,
				"description": "触发本次采集的审计来源，不参与线上字段发现。",
			},
			"expected_active_generation": map[string]any{
				"type": "string", "pattern": "^(none|[a-f0-9]{64})$",
				"description": "调用前由 wecom_schema_registry_status 读到的 active generation；尚未初始化时使用 none。用于并发变更保护。",
			},
			"owner_authorization": map[string]any{"const": schemaRegistryAuthorization},
		},
	}
}

func (s *Server) schemaRegistryStatus(ctx context.Context, runtime config.Config, client wecom.Requester, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	table, err := loadSchemaRegistryTable(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	state := "initialized_empty"
	if table.ActiveGeneration != "" && table.ActiveComplete {
		state = "active"
	} else if table.ActiveGeneration != "" {
		state = "active_generation_incomplete"
	}
	return map[string]any{
		"state":                      state,
		"instance_name":              runtime.InstanceName,
		"registry_key":               runtime.RegistryKey,
		"sheet_title":                schemaRegistrySheetTitle,
		"active_generation":          emptyGeneration(table.ActiveGeneration),
		"active_generation_complete": table.ActiveComplete,
		"active_field_count":         table.ActiveFieldCount,
		"record_count":               len(table.Records),
		"enterprise_wecom_updated":   false,
		"local_mirror_updated":       false,
	}, nil
}

func (s *Server) updateSchemaRegistry(ctx context.Context, runtime config.Config, client wecom.Requester, raw json.RawMessage) (any, error) {
	var input struct {
		IdempotencyKey           string `json:"idempotency_key"`
		SourceRevision           string `json:"source_revision"`
		ExpectedActiveGeneration string `json:"expected_active_generation"`
		OwnerAuthorization       string `json:"owner_authorization"`
	}
	if err := strictDecode(raw, &input, "idempotency_key", "source_revision", "expected_active_generation", "owner_authorization"); err != nil {
		return nil, err
	}
	if len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 || len(input.SourceRevision) < 1 || len(input.SourceRevision) > 256 {
		return nil, fmt.Errorf("Schema Registry 更新参数长度无效")
	}
	if input.OwnerAuthorization != schemaRegistryAuthorization {
		return nil, fmt.Errorf("缺少明确的在线 Schema Registry 更新授权")
	}
	if input.ExpectedActiveGeneration != "none" && !initializeSHA256Digest.MatchString(input.ExpectedActiveGeneration) {
		return nil, fmt.Errorf("expected_active_generation 必须是 none 或 64 位摘要")
	}
	if err := verifySchemaAdmin(runtime); err != nil {
		return nil, err
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_records", "update_records"} {
		if !runtime.AllowsInGroup(schemaRegistryGroup, operation) {
			return nil, fmt.Errorf("Schema Registry 白名单未允许 %s", operation)
		}
	}
	s.schemaRegistryMu.Lock()
	defer s.schemaRegistryMu.Unlock()
	release, err := acquireProgressFileLock(ctx, runtime.StatePath+".schema-registry")
	if err != nil {
		return nil, fmt.Errorf("另一个 Schema Registry 更新正在执行")
	}
	defer release()

	table, err := loadSchemaRegistryTable(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	current := emptyGeneration(table.ActiveGeneration)
	if current != input.ExpectedActiveGeneration {
		return nil, fmt.Errorf("active generation 已变化：期望 %s，实际 %s；请重新读取状态", input.ExpectedActiveGeneration, current)
	}
	snapshot, err := collectSchemaRegistrySnapshot(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	if err := adoptSchemaRegistryGenerationMetadata(&snapshot, table, input.SourceRevision); err != nil {
		return nil, err
	}

	digestData, _ := json.Marshal(map[string]any{
		"idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision,
		"expected_active_generation": current, "new_generation": snapshot.Generation,
	})
	digestSum := sha256.Sum256(digestData)
	requestDigest := hex.EncodeToString(digestSum[:])
	if table.ActiveGeneration == snapshot.Generation && table.ActiveComplete {
		recovered, err := s.completeSchemaRegistryReservationIfPresent(runtime.StatePath, input.IdempotencyKey, requestDigest, runtime.WecomOperatorUserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"state": "already_current", "generation": snapshot.Generation, "schema_digest": snapshot.Generation,
			"field_count": snapshot.FieldCount, "readback_verified": true, "active_pointer_changed": false,
			"idempotency_recovered": recovered, "local_mirror_updated": false,
		}, nil
	}
	if err := s.reserveSchemaRegistryWithOperator(runtime.StatePath, input.IdempotencyKey, requestDigest, runtime.WecomOperatorUserID); err != nil {
		return nil, err
	}

	expected := schemaRegistryGenerationRecords(snapshot, runtime, input.SourceRevision, table.FieldIDs)
	missing, err := schemaRegistryMissingRecords(table.ByKey, expected, table.FieldIDs)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(missing); start += schemaRegistryBatchSize {
		end := start + schemaRegistryBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		response, requestErr := client.Request(ctx, "add_records", map[string]any{
			"docid": table.Target.DocumentID, "sheet_id": table.Target.SheetID,
			"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": missing[start:end],
		})
		if requestErr != nil || apiError(response) != nil {
			refreshed, readErr := loadSchemaRegistryTable(ctx, runtime, client)
			if readErr == nil {
				remaining, compareErr := schemaRegistryMissingRecords(refreshed.ByKey, expected, refreshed.FieldIDs)
				if compareErr == nil && len(remaining) == 0 {
					table = refreshed
					continue
				}
			}
			return map[string]any{
				"state": "generation_write_readback_pending", "generation": snapshot.Generation,
				"readback_verified": false, "active_pointer_changed": false,
				"recovery": "重新读取 wecom_schema_registry_status；不得绕过 active 指针或手工补写记录",
			}, nil
		}
	}

	table, err = loadSchemaRegistryTable(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	remaining, err := schemaRegistryMissingRecords(table.ByKey, expected, table.FieldIDs)
	if err != nil {
		return nil, err
	}
	if len(remaining) != 0 {
		return map[string]any{
			"state": "generation_write_readback_pending", "generation": snapshot.Generation,
			"missing_record_count": len(remaining), "readback_verified": false, "active_pointer_changed": false,
		}, nil
	}

	pointerValues := schemaRegistryPointerValues(snapshot, runtime, input.SourceRevision, table.FieldIDs)
	pointerRecord := map[string]any{"values": pointerValues}
	operation := "add_records"
	if table.ActiveRecordID != "" {
		operation = "update_records"
		pointerRecord["record_id"] = table.ActiveRecordID
	}
	response, requestErr := client.Request(ctx, operation, map[string]any{
		"docid": table.Target.DocumentID, "sheet_id": table.Target.SheetID,
		"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []any{pointerRecord},
	})
	if requestErr != nil || apiError(response) != nil {
		refreshed, readErr := loadSchemaRegistryTable(ctx, runtime, client)
		if readErr != nil || refreshed.ActiveGeneration != snapshot.Generation || !refreshed.ActiveComplete {
			return map[string]any{
				"state": "active_pointer_readback_pending", "generation": snapshot.Generation,
				"readback_verified": false, "active_pointer_changed": false,
			}, nil
		}
		table = refreshed
	} else {
		table, err = loadSchemaRegistryTable(ctx, runtime, client)
		if err != nil || table.ActiveGeneration != snapshot.Generation || !table.ActiveComplete {
			return map[string]any{
				"state": "active_pointer_readback_pending", "generation": snapshot.Generation,
				"readback_verified": false, "active_pointer_changed": false,
			}, nil
		}
	}
	if err := s.completeStateWithOperator(runtime.StatePath, input.IdempotencyKey, requestDigest, runtime.WecomOperatorUserID); err != nil {
		return map[string]any{
			"state": "applied_idempotency_completion_pending", "generation": snapshot.Generation,
			"readback_verified": true, "active_pointer_changed": true, "idempotency_error": err.Error(),
		}, nil
	}
	return map[string]any{
		"state": "applied", "generation": snapshot.Generation, "schema_digest": snapshot.Generation,
		"captured_at": snapshot.CapturedAt, "table_count": 9, "field_count": snapshot.FieldCount,
		"readback_verified": true, "active_pointer_changed": true, "local_mirror_updated": false,
	}, nil
}

func adoptSchemaRegistryGenerationMetadata(snapshot *schemaRegistrySnapshot, table schemaRegistryTable, sourceRevision string) error {
	manifest := table.ByKey["manifest:"+snapshot.Generation]
	if manifest == nil {
		return nil
	}
	values, _ := manifest["values"].(map[string]any)
	existingSource := initializeTextCell(values[table.FieldIDs["来源修订"]])
	existingCapturedAt := initializeTextCell(values[table.FieldIDs["捕获时间"]])
	if existingSource != sourceRevision {
		return fmt.Errorf("同一 Schema generation 已绑定其他来源修订")
	}
	if existingCapturedAt == "" {
		return fmt.Errorf("同一 Schema generation 的 manifest 缺少捕获时间")
	}
	snapshot.CapturedAt = existingCapturedAt
	return nil
}

// Schema Registry generations have stable online natural keys, so an
// interrupted call can safely continue with the same idempotency key and
// request digest. The generic business-write reservation intentionally blocks
// all pending retries and is therefore too strict for this resumable state
// machine.
func (s *Server) reserveSchemaRegistryWithOperator(path, key, digest, operator string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(path)
	if err != nil {
		return fmt.Errorf("获取幂等状态锁失败")
	}
	defer release()
	state, err := loadState(path)
	if err != nil {
		return err
	}
	if old, found := state.Entries[key]; found {
		if old.Digest != digest || old.BusinessOperatorUserID != operator {
			return fmt.Errorf("idempotency_key 已绑定其他 Schema Registry 更新")
		}
		if old.Status == "completed" {
			return fmt.Errorf("此 Schema Registry 更新已完成")
		}
		return nil
	}
	state.Entries[key] = stateEntry{Digest: digest, Status: "pending", BusinessOperatorUserID: operator}
	return saveState(path, state)
}

func (s *Server) completeSchemaRegistryReservationIfPresent(path, key, digest, operator string) (bool, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(path)
	if err != nil {
		return false, fmt.Errorf("获取幂等状态锁失败")
	}
	defer release()
	state, err := loadState(path)
	if err != nil {
		return false, err
	}
	old, found := state.Entries[key]
	if !found {
		return false, nil
	}
	if old.Digest != digest || old.BusinessOperatorUserID != operator {
		return false, fmt.Errorf("idempotency_key 已绑定其他 Schema Registry 更新")
	}
	if old.Status == "completed" {
		return false, nil
	}
	state.Entries[key] = stateEntry{Digest: digest, Status: "completed", BusinessOperatorUserID: operator}
	if err := saveState(path, state); err != nil {
		return false, fmt.Errorf("保存幂等完成状态失败")
	}
	return true, nil
}

func emptyGeneration(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func loadSchemaRegistryTable(ctx context.Context, runtime config.Config, client wecom.Requester) (schemaRegistryTable, error) {
	anchor, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S01", runtime.Allows)
	if err != nil {
		return schemaRegistryTable{}, err
	}
	sheetID, err := resolveExactSheetIDWithRequester(ctx, client, anchor.DocumentID, schemaRegistrySheetTitle)
	if err != nil {
		return schemaRegistryTable{}, fmt.Errorf("Z-S00 尚未完成内置迁移: %w", err)
	}
	target := wecom.Target{DocumentID: anchor.DocumentID, SheetID: sheetID, Role: schemaRegistryRole}
	fields, err := wecom.ReadFields(ctx, client, target, runtime.Allows)
	if err != nil {
		return schemaRegistryTable{}, err
	}
	expected := schemaRegistryMigrationFields()
	fieldIDs := map[string]string{}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		id, _ := field["field_id"].(string)
		typeName, _ := field["field_type"].(string)
		if title != "" {
			if fieldIDs[title] != "" {
				return schemaRegistryTable{}, fmt.Errorf("Z-S00 字段 %s 不唯一", title)
			}
			if typeName != "FIELD_TYPE_TEXT" || id == "" {
				return schemaRegistryTable{}, fmt.Errorf("Z-S00 字段 %s 不是已登记文本字段", title)
			}
			fieldIDs[title] = id
		}
	}
	if len(fields) != len(expected) {
		return schemaRegistryTable{}, fmt.Errorf("Z-S00 字段数量不符合内置迁移：实际 %d，期望 %d", len(fields), len(expected))
	}
	for _, field := range expected {
		if fieldIDs[field.Title] == "" {
			return schemaRegistryTable{}, fmt.Errorf("Z-S00 缺少字段 %s", field.Title)
		}
	}
	records, complete, err := readAllInitializeRecords(ctx, client, anchor.DocumentID, sheetID)
	if err != nil || !complete {
		return schemaRegistryTable{}, fmt.Errorf("Z-S00 完整记录回读失败")
	}
	byKey := map[string]map[string]any{}
	activeGeneration, activeRecordID := "", ""
	for _, rawRecord := range records {
		record, _ := rawRecord.(map[string]any)
		values, _ := record["values"].(map[string]any)
		key := initializeTextCell(values[fieldIDs["Schema 条目键"]])
		if key == "" {
			return schemaRegistryTable{}, fmt.Errorf("Z-S00 存在缺少 Schema 条目键的记录")
		}
		if byKey[key] != nil {
			return schemaRegistryTable{}, fmt.Errorf("Z-S00 Schema 条目键 %s 不唯一", key)
		}
		byKey[key] = record
		if key == schemaRegistryActiveKey {
			activeGeneration = initializeTextCell(values[fieldIDs["Schema 版本"]])
			activeRecordID, _ = record["record_id"].(string)
			if !initializeSHA256Digest.MatchString(activeGeneration) ||
				initializeTextCell(values[fieldIDs["条目类型"]]) != "active_pointer" ||
				initializeTextCell(values[fieldIDs["生效状态"]]) != "active" ||
				initializeTextCell(values[fieldIDs["Schema Digest"]]) != activeGeneration ||
				initializeTextCell(values[fieldIDs["实例名称"]]) != runtime.InstanceName ||
				initializeTextCell(values[fieldIDs["Registry Key"]]) != runtime.RegistryKey {
				return schemaRegistryTable{}, fmt.Errorf("Z-S00 @active 指针无效")
			}
		}
	}
	activeComplete, activeFieldCount := schemaRegistryGenerationComplete(byKey, fieldIDs, activeGeneration)
	return schemaRegistryTable{
		Target: target, FieldIDs: fieldIDs, Records: records, ByKey: byKey,
		ActiveGeneration: activeGeneration, ActiveRecordID: activeRecordID,
		ActiveComplete: activeComplete, ActiveFieldCount: activeFieldCount,
	}, nil
}

func resolveExactSheetIDWithRequester(ctx context.Context, client wecom.Requester, documentID, title string) (string, error) {
	response, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil {
		return "", err
	}
	if err := apiError(response); err != nil {
		return "", err
	}
	matches := []string{}
	for _, raw := range resultSlice(response, "sheet_list") {
		sheet, _ := raw.(map[string]any)
		name := schemaRegistrySheetName(sheet)
		id, _ := sheet["sheet_id"].(string)
		if name == title && id != "" {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("目标子表 %s 未找到", title)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("目标子表 %s 不唯一", title)
	}
	return matches[0], nil
}

func schemaRegistrySheetName(sheet map[string]any) string {
	for _, key := range []string{"name", "sheet_name", "title"} {
		if value, ok := sheet[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func collectSchemaRegistrySnapshot(ctx context.Context, runtime config.Config, client wecom.Requester) (schemaRegistrySnapshot, error) {
	roles := make([]string, 0, len(validRoles))
	for role := range validRoles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	targets, err := wecom.ResolveTargets(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, roles, runtime.Allows)
	if err != nil {
		return schemaRegistrySnapshot{}, err
	}
	sheetsResponse, err := client.Request(ctx, "get_sheet", map[string]any{"docid": targets[roles[0]].DocumentID})
	if err != nil || apiError(sheetsResponse) != nil {
		return schemaRegistrySnapshot{}, fmt.Errorf("读取九表名称失败")
	}
	sheetNames := map[string]string{}
	for _, raw := range resultSlice(sheetsResponse, "sheet_list") {
		sheet, _ := raw.(map[string]any)
		id, _ := sheet["sheet_id"].(string)
		if id != "" {
			sheetNames[id] = schemaRegistrySheetName(sheet)
		}
	}
	entries := []map[string]string{}
	for _, role := range roles {
		target := targets[role]
		rawFields, err := wecom.ReadFields(ctx, client, target, runtime.Allows)
		if err != nil {
			return schemaRegistrySnapshot{}, err
		}
		fields, err := mirrorFields(rawFields)
		if err != nil {
			return schemaRegistrySnapshot{}, fmt.Errorf("%s: %w", role, err)
		}
		if err := validateUniqueFields(fields); err != nil {
			return schemaRegistrySnapshot{}, fmt.Errorf("%s: %w", role, err)
		}
		rawByID := map[string]map[string]any{}
		for _, raw := range rawFields {
			id, _ := raw["field_id"].(string)
			rawByID[id] = raw
		}
		for _, field := range fields {
			codec, status, writable := schemaRegistryCodec(field.Type)
			entry := map[string]string{
				"条目类型": "field", "实例名称": runtime.InstanceName, "Registry Key": runtime.RegistryKey,
				"表角色": role, "表名": sheetNames[target.SheetID], "表 ID": target.SheetID,
				"字段名": field.Title, "字段 ID": field.ID, "字段类型": field.Type,
				"是否主字段": schemaRegistryPrimary(rawByID[field.ID]), "是否多值": schemaRegistryMultiple(field.ReferenceIsMultiple),
				"选项定义": schemaRegistryOptions(field.Options), "关联目标表 ID": field.ReferenceTargetSheetID,
				"关联目标字段 ID": field.ReferenceTargetFieldID, "原始字段属性": schemaRegistryProperties(rawByID[field.ID]),
				"是否系统字段": yesNo(schemaRegistrySystemField(field.Type)), "是否允许新增": yesNo(writable),
				"是否允许更新": yesNo(writable), "写入编码器": codec, "Codec 验证状态": status,
			}
			if entry["表名"] == "" {
				return schemaRegistrySnapshot{}, fmt.Errorf("%s 表名回读失败", role)
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i]["表角色"] + "\x00" + entries[i]["字段 ID"]
		right := entries[j]["表角色"] + "\x00" + entries[j]["字段 ID"]
		return left < right
	})
	digest, err := schemaRegistryEntriesDigest(entries)
	if err != nil {
		return schemaRegistrySnapshot{}, err
	}
	return schemaRegistrySnapshot{
		Generation: digest, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		FieldCount: len(entries), Entries: entries,
	}, nil
}

func schemaRegistryEntriesDigest(entries []map[string]string) (string, error) {
	ordered := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		normalized := map[string]string{}
		for _, title := range schemaRegistryDigestFields {
			normalized[title] = entry[title]
		}
		ordered = append(ordered, normalized)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left := ordered[i]["表角色"] + "\x00" + ordered[i]["字段 ID"]
		right := ordered[j]["表角色"] + "\x00" + ordered[j]["字段 ID"]
		return left < right
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func schemaRegistryCodec(fieldType string) (string, string, bool) {
	switch fieldType {
	case "FIELD_TYPE_TEXT":
		return "text_cell_array", "已验证", true
	case "FIELD_TYPE_CHECKBOX":
		return "boolean", "已验证", true
	case "FIELD_TYPE_SINGLE_SELECT":
		return "option_text_array", "已验证", true
	case "FIELD_TYPE_NUMBER", "FIELD_TYPE_PROGRESS", "FIELD_TYPE_CURRENCY", "FIELD_TYPE_PERCENTAGE":
		return "number", "已验证", true
	case "FIELD_TYPE_DATE_TIME", "FIELD_TYPE_PHONE_NUMBER", "FIELD_TYPE_EMAIL", "FIELD_TYPE_BARCODE":
		return "string", "已验证", true
	case "FIELD_TYPE_REFERENCE":
		return "record_id_array", "已验证", true
	case "FIELD_TYPE_IMAGE", "FIELD_TYPE_USER", "FIELD_TYPE_SELECT", "FIELD_TYPE_MULTI_SELECT", "FIELD_TYPE_URL", "FIELD_TYPE_LOCATION", "FIELD_TYPE_WWGROUP":
		return "verified_cell_object_array", "已验证", true
	case "FIELD_TYPE_AUTONUMBER", "FIELD_TYPE_CREATED_USER", "FIELD_TYPE_MODIFIED_USER", "FIELD_TYPE_CREATED_TIME", "FIELD_TYPE_MODIFIED_TIME":
		return "system_read_only", "系统只读", false
	case "FIELD_TYPE_ATTACHMENT":
		return "unsupported", "公开写入契约不支持", false
	default:
		return "unsupported", "未验证", false
	}
}

func schemaRegistrySystemField(fieldType string) bool {
	switch fieldType {
	case "FIELD_TYPE_AUTONUMBER", "FIELD_TYPE_CREATED_USER", "FIELD_TYPE_MODIFIED_USER", "FIELD_TYPE_CREATED_TIME", "FIELD_TYPE_MODIFIED_TIME", "FIELD_TYPE_FORMULA", "FIELD_TYPE_LOOKUP":
		return true
	default:
		return false
	}
}

func schemaRegistryPrimary(raw map[string]any) string {
	for _, key := range []string{"is_primary", "is_primary_field", "primary"} {
		if value, ok := raw[key].(bool); ok {
			return yesNo(value)
		}
	}
	return "未知"
}

func schemaRegistryMultiple(value *bool) string {
	if value == nil {
		return "不适用"
	}
	return yesNo(*value)
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func schemaRegistryOptions(options map[string]string) string {
	if len(options) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(options)
	return string(encoded)
}

func schemaRegistryProperties(raw map[string]any) string {
	properties := map[string]any{}
	for key, value := range raw {
		if strings.HasPrefix(key, "property_") || key == "is_primary" || key == "is_primary_field" || key == "primary" {
			properties[key] = value
		}
	}
	if len(properties) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(properties)
	return string(encoded)
}

func schemaRegistryGenerationRecords(snapshot schemaRegistrySnapshot, runtime config.Config, sourceRevision string, fieldIDs map[string]string) map[string]map[string]any {
	result := map[string]map[string]any{}
	manifest := map[string]string{
		"Schema 条目键": "manifest:" + snapshot.Generation, "条目类型": "manifest", "Schema 版本": snapshot.Generation,
		"生效状态": "ready", "实例名称": runtime.InstanceName, "Registry Key": runtime.RegistryKey,
		"来源修订": sourceRevision, "捕获时间": snapshot.CapturedAt, "Schema Digest": snapshot.Generation,
		"表总数": "9", "字段总数": strconv.Itoa(snapshot.FieldCount),
	}
	result[manifest["Schema 条目键"]] = schemaRegistryWireValues(manifest, fieldIDs)
	for _, base := range snapshot.Entries {
		entry := map[string]string{}
		for key, value := range base {
			entry[key] = value
		}
		entry["Schema 条目键"] = snapshot.Generation + ":" + entry["表角色"] + ":" + entry["字段 ID"]
		entry["Schema 版本"] = snapshot.Generation
		entry["生效状态"] = "ready"
		entry["来源修订"] = sourceRevision
		entry["捕获时间"] = snapshot.CapturedAt
		entry["Schema Digest"] = snapshot.Generation
		result[entry["Schema 条目键"]] = schemaRegistryWireValues(entry, fieldIDs)
	}
	return result
}

func schemaRegistryPointerValues(snapshot schemaRegistrySnapshot, runtime config.Config, sourceRevision string, fieldIDs map[string]string) map[string]any {
	return schemaRegistryWireValues(map[string]string{
		"Schema 条目键": schemaRegistryActiveKey, "条目类型": "active_pointer", "Schema 版本": snapshot.Generation,
		"生效状态": "active", "实例名称": runtime.InstanceName, "Registry Key": runtime.RegistryKey,
		"来源修订": sourceRevision, "捕获时间": snapshot.CapturedAt, "Schema Digest": snapshot.Generation,
		"表总数": "9", "字段总数": strconv.Itoa(snapshot.FieldCount),
	}, fieldIDs)
}

func schemaRegistryWireValues(values map[string]string, fieldIDs map[string]string) map[string]any {
	result := map[string]any{}
	for title, value := range values {
		if value == "" {
			continue
		}
		result[fieldIDs[title]] = []any{map[string]any{"type": "text", "text": value}}
	}
	return result
}

func schemaRegistryMissingRecords(existing map[string]map[string]any, expected map[string]map[string]any, fieldIDs map[string]string) ([]any, error) {
	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	missing := []any{}
	for _, key := range keys {
		record := existing[key]
		if record == nil {
			missing = append(missing, map[string]any{"values": expected[key]})
			continue
		}
		values, _ := record["values"].(map[string]any)
		for _, fieldID := range fieldIDs {
			wantedText := initializeTextCell(expected[key][fieldID])
			if initializeTextCell(values[fieldID]) != wantedText {
				return nil, fmt.Errorf("Z-S00 已有条目 %s 与当前不可变 generation 内容冲突", key)
			}
		}
	}
	return missing, nil
}

func schemaRegistryGenerationComplete(byKey map[string]map[string]any, fieldIDs map[string]string, generation string) (bool, int) {
	if generation == "" {
		return false, 0
	}
	manifest := byKey["manifest:"+generation]
	if manifest == nil {
		return false, 0
	}
	manifestValues, _ := manifest["values"].(map[string]any)
	expected, err := strconv.Atoi(initializeTextCell(manifestValues[fieldIDs["字段总数"]]))
	tableCount, tableErr := strconv.Atoi(initializeTextCell(manifestValues[fieldIDs["表总数"]]))
	if err != nil || tableErr != nil || expected < 1 || tableCount != 9 ||
		initializeTextCell(manifestValues[fieldIDs["条目类型"]]) != "manifest" ||
		initializeTextCell(manifestValues[fieldIDs["Schema 版本"]]) != generation ||
		initializeTextCell(manifestValues[fieldIDs["生效状态"]]) != "ready" ||
		initializeTextCell(manifestValues[fieldIDs["Schema Digest"]]) != generation {
		return false, 0
	}
	count := 0
	roles := map[string]bool{}
	digestEntries := []map[string]string{}
	for key, record := range byKey {
		values, _ := record["values"].(map[string]any)
		if initializeTextCell(values[fieldIDs["条目类型"]]) != "field" || initializeTextCell(values[fieldIDs["Schema 版本"]]) != generation {
			continue
		}
		role := initializeTextCell(values[fieldIDs["表角色"]])
		fieldID := initializeTextCell(values[fieldIDs["字段 ID"]])
		_, validRole := validRoles[role]
		if !validRole || fieldID == "" || key != generation+":"+role+":"+fieldID ||
			initializeTextCell(values[fieldIDs["表名"]]) == "" ||
			initializeTextCell(values[fieldIDs["表 ID"]]) == "" ||
			initializeTextCell(values[fieldIDs["字段名"]]) == "" ||
			initializeTextCell(values[fieldIDs["字段类型"]]) == "" ||
			initializeTextCell(values[fieldIDs["生效状态"]]) != "ready" ||
			initializeTextCell(values[fieldIDs["Schema Digest"]]) != generation {
			return false, count
		}
		roles[role] = true
		digestEntry := map[string]string{}
		for _, title := range schemaRegistryDigestFields {
			digestEntry[title] = initializeTextCell(values[fieldIDs[title]])
		}
		digestEntries = append(digestEntries, digestEntry)
		count++
	}
	if count != expected || len(roles) != tableCount {
		return false, count
	}
	sort.Slice(digestEntries, func(i, j int) bool {
		left := digestEntries[i]["表角色"] + "\x00" + digestEntries[i]["字段 ID"]
		right := digestEntries[j]["表角色"] + "\x00" + digestEntries[j]["字段 ID"]
		return left < right
	})
	digest, err := schemaRegistryEntriesDigest(digestEntries)
	if err != nil {
		return false, count
	}
	return digest == generation, count
}
