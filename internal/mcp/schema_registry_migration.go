package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

const (
	schemaRegistryRole       = "Z-S00"
	schemaRegistrySheetTitle = "Z-S00｜Schema Registry"
)

type schemaRegistryMigrationPlan struct {
	State           string
	DocumentID      string
	SheetID         string
	SheetExists     bool
	DefaultField    map[string]any
	MissingFields   []schemaMigrationField
	Operations      []map[string]any
	CurrentSummary  map[string]any
	ProposedSummary map[string]any
	PreviewID       string
}

func schemaRegistryMigrationFields() []schemaMigrationField {
	titles := []string{
		"Schema 条目键", "条目类型", "Schema 版本", "生效状态", "实例名称", "Registry Key",
		"表角色", "表名", "表 ID", "字段名", "字段 ID", "字段类型", "是否主字段", "是否多值",
		"选项定义", "关联目标表 ID", "关联目标字段 ID", "原始字段属性", "是否系统字段",
		"是否允许新增", "是否允许更新", "写入编码器", "Codec 验证状态", "来源修订", "捕获时间", "Schema Digest",
		"表总数", "字段总数",
	}
	fields := make([]schemaMigrationField, 0, len(titles))
	for _, title := range titles {
		fields = append(fields, schemaMigrationField{Title: title, Type: "FIELD_TYPE_TEXT"})
	}
	return fields
}

func buildSchemaRegistryMigrationPlan(ctx context.Context, runtime config.Config, client *wecom.Client) (schemaRegistryMigrationPlan, error) {
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_sheet", "add_fields", "update_fields"} {
		if !runtime.AllowsInGroup(schemaMigrationGroup, operation) {
			return schemaRegistryMigrationPlan{}, fmt.Errorf("Schema 迁移白名单未允许 %s", operation)
		}
	}
	anchor, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S01", runtime.Allows)
	if err != nil {
		return schemaRegistryMigrationPlan{}, err
	}
	expected := schemaRegistryMigrationFields()
	plan := schemaRegistryMigrationPlan{
		DocumentID:      anchor.DocumentID,
		CurrentSummary:  map[string]any{},
		ProposedSummary: map[string]any{"target_role": schemaRegistryRole, "sheet_title": schemaRegistrySheetTitle, "field_count": len(expected)},
	}
	sheetID, sheetErr := resolveExactSheetID(ctx, client, anchor.DocumentID, schemaRegistrySheetTitle)
	if sheetErr != nil {
		if !strings.Contains(sheetErr.Error(), "未找到") {
			return schemaRegistryMigrationPlan{}, sheetErr
		}
		plan.State = "ready"
		plan.CurrentSummary = map[string]any{"sheet_exists": false, "field_count": 0}
		plan.MissingFields = expected
		plan.Operations = []map[string]any{
			{"operation": "add_sheet", "before": "不存在", "after": schemaRegistrySheetTitle},
			{"operation": "update_fields", "before": "新子表默认主字段", "after": "Schema 条目键（文本）"},
			{"operation": "add_fields", "before": 0, "after": len(expected) - 1, "field_titles": migrationFieldTitles(expected[1:])},
		}
		plan.PreviewID = schemaRegistryMigrationPreviewID(runtime, plan)
		return plan, nil
	}

	plan.SheetExists, plan.SheetID = true, sheetID
	fields, err := readMigrationFields(ctx, client, anchor.DocumentID, sheetID)
	if err != nil {
		return schemaRegistryMigrationPlan{}, err
	}
	defaultField, missing, err := assessSchemaRegistryFields(ctx, client, anchor.DocumentID, sheetID, expected, fields)
	if err != nil {
		return schemaRegistryMigrationPlan{}, err
	}
	plan.DefaultField, plan.MissingFields = defaultField, missing
	plan.CurrentSummary = map[string]any{"sheet_exists": true, "sheet_title": schemaRegistrySheetTitle, "field_count": len(fields), "field_titles": fieldTitles(fields)}
	if defaultField != nil {
		plan.Operations = append(plan.Operations, map[string]any{"operation": "update_fields", "before": defaultField["field_title"], "after": "Schema 条目键（文本）"})
	}
	fieldsToAdd := missing
	if defaultField != nil {
		fieldsToAdd = removeMigrationField(fieldsToAdd, "Schema 条目键")
	}
	if len(fieldsToAdd) > 0 {
		plan.Operations = append(plan.Operations, map[string]any{"operation": "add_fields", "before": 0, "after": len(fieldsToAdd), "field_titles": migrationFieldTitles(fieldsToAdd)})
	}
	if len(plan.Operations) == 0 {
		plan.State = "up_to_date"
	} else {
		plan.State = "ready"
	}
	plan.PreviewID = schemaRegistryMigrationPreviewID(runtime, plan)
	return plan, nil
}

func assessSchemaRegistryFields(ctx context.Context, client *wecom.Client, documentID, sheetID string, expected []schemaMigrationField, fields []map[string]any) (map[string]any, []schemaMigrationField, error) {
	byTitle := map[string]map[string]any{}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		if title == "" {
			continue
		}
		if byTitle[title] != nil {
			return nil, nil, fmt.Errorf("Z-S00 存在重复字段 %s，拒绝迁移", title)
		}
		byTitle[title] = field
	}
	missing := []schemaMigrationField{}
	for _, wanted := range expected {
		current := byTitle[wanted.Title]
		if current == nil {
			missing = append(missing, wanted)
			continue
		}
		if current["field_type"] != wanted.Type {
			return nil, nil, fmt.Errorf("Z-S00 字段 %s 类型不兼容：线上 %v，迁移要求 %s", wanted.Title, current["field_type"], wanted.Type)
		}
	}
	if len(fields) == 1 && len(missing) == len(expected) {
		if fields[0]["field_type"] != "FIELD_TYPE_TEXT" {
			return nil, nil, fmt.Errorf("Z-S00 默认主字段不是文本，拒绝自动调整")
		}
		response, err := client.Request(ctx, "get_records", map[string]any{"docid": documentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 1})
		if err != nil || apiError(response) != nil || len(recordsFrom(response)) != 0 {
			return nil, nil, fmt.Errorf("Z-S00 已存在非空或状态不明，拒绝把现有字段视为迁移默认字段")
		}
		return fields[0], missing, nil
	}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		if title != "" && !containsMigrationField(expected, title) {
			return nil, nil, fmt.Errorf("Z-S00 存在迁移目录外字段 %s，拒绝覆盖现有结构", title)
		}
	}
	return nil, missing, nil
}

func (s *Server) applySchemaRegistryMigration(ctx context.Context, runtime config.Config, client *wecom.Client, previewID string) (any, error) {
	plan, err := buildSchemaRegistryMigrationPlan(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	if previewID != plan.PreviewID {
		return nil, fmt.Errorf("Schema 迁移预览已失效，请重新预览当前线上结构")
	}
	if len(plan.Operations) == 0 {
		result := publicSchemaRegistryMigrationPlan(plan)
		result["state"] = "already_applied"
		result["readback_verified"] = true
		return result, nil
	}
	reservationKey := "schema-migration:" + schemaRegistryMigrationID + ":" + previewID
	if err := s.reserveWithOperator(runtime.StatePath, reservationKey, previewID, runtime.WecomOperatorUserID); err != nil {
		return nil, err
	}

	sheetID := plan.SheetID
	if !plan.SheetExists {
		response, requestErr := client.Request(ctx, "add_sheet", map[string]any{"docid": plan.DocumentID, "properties": map[string]any{"title": schemaRegistrySheetTitle}})
		if requestErr != nil {
			return nil, requestErr
		}
		if err := apiError(response); err != nil {
			return nil, fmt.Errorf("创建 Z-S00 Schema Registry 子表未确认成功: %w", err)
		}
		sheetID, err = resolveExactSheetID(ctx, client, plan.DocumentID, schemaRegistrySheetTitle)
		if err != nil {
			return withOperatorAudit(map[string]any{"state": "applied_readback_pending", "migration_id": schemaRegistryMigrationID, "preview_id": previewID, "readback_verified": false}, runtime.WecomOperatorUserID), nil
		}
	}

	fields, err := readMigrationFields(ctx, client, plan.DocumentID, sheetID)
	if err != nil {
		return withOperatorAudit(map[string]any{"state": "applied_readback_pending", "migration_id": schemaRegistryMigrationID, "preview_id": previewID, "readback_verified": false}, runtime.WecomOperatorUserID), nil
	}
	expected := schemaRegistryMigrationFields()
	defaultField, missing, err := assessSchemaRegistryFields(ctx, client, plan.DocumentID, sheetID, expected, fields)
	if err != nil {
		return nil, err
	}
	if defaultField != nil {
		fieldID, _ := defaultField["field_id"].(string)
		response, requestErr := client.Request(ctx, "update_fields", map[string]any{"docid": plan.DocumentID, "sheet_id": sheetID, "fields": []any{map[string]any{"field_id": fieldID, "field_title": "Schema 条目键", "field_type": "FIELD_TYPE_TEXT"}}})
		if requestErr != nil {
			return nil, requestErr
		}
		if err := apiError(response); err != nil {
			return nil, fmt.Errorf("配置 Z-S00 主字段未确认成功: %w", err)
		}
		missing = removeMigrationField(missing, "Schema 条目键")
	}
	if len(missing) > 0 {
		wireFields := make([]any, 0, len(missing))
		for _, field := range missing {
			wireFields = append(wireFields, migrationFieldWire(field))
		}
		response, requestErr := client.Request(ctx, "add_fields", map[string]any{"docid": plan.DocumentID, "sheet_id": sheetID, "fields": wireFields})
		if requestErr != nil {
			return nil, requestErr
		}
		if err := apiError(response); err != nil {
			return nil, fmt.Errorf("新增 Z-S00 字段未确认成功: %w", err)
		}
	}

	readback, err := buildSchemaRegistryMigrationPlan(ctx, runtime, client)
	if err != nil || len(readback.Operations) != 0 {
		return withOperatorAudit(map[string]any{"state": "applied_readback_pending", "migration_id": schemaRegistryMigrationID, "preview_id": previewID, "readback_verified": false}, runtime.WecomOperatorUserID), nil
	}
	if err := s.completeStateWithOperator(runtime.StatePath, reservationKey, previewID, runtime.WecomOperatorUserID); err != nil {
		return withOperatorAudit(map[string]any{"state": "applied_idempotency_completion_pending", "migration_id": schemaRegistryMigrationID, "preview_id": previewID, "readback_verified": true, "idempotency_error": err.Error()}, runtime.WecomOperatorUserID), nil
	}
	return withOperatorAudit(map[string]any{
		"state":                   "applied",
		"migration_id":            schemaRegistryMigrationID,
		"preview_id":              previewID,
		"target_role":             schemaRegistryRole,
		"sheet_title":             schemaRegistrySheetTitle,
		"field_count":             readback.CurrentSummary["field_count"],
		"readback_verified":       true,
		"local_mirror_updated":    false,
		"next_required_operation": "调用 wecom_schema_registry_update 生成首个在线 Schema generation",
	}, runtime.WecomOperatorUserID), nil
}

func schemaRegistryMigrationPreviewID(runtime config.Config, plan schemaRegistryMigrationPlan) string {
	data, _ := json.Marshal(map[string]any{
		"migration_id":  schemaRegistryMigrationID,
		"config_digest": runtime.Digest(),
		"document_id":   plan.DocumentID,
		"sheet_id":      plan.SheetID,
		"current":       plan.CurrentSummary,
		"proposed":      plan.ProposedSummary,
		"operations":    plan.Operations,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func publicSchemaRegistryMigrationPlan(plan schemaRegistryMigrationPlan) map[string]any {
	return map[string]any{
		"state":                    plan.State,
		"migration_id":             schemaRegistryMigrationID,
		"target_role":              schemaRegistryRole,
		"sheet_title":              schemaRegistrySheetTitle,
		"current":                  plan.CurrentSummary,
		"proposed":                 plan.ProposedSummary,
		"operations":               plan.Operations,
		"impact":                   "仅新增 Z-S00 Schema Registry 及固定目录字段；不删除记录，不改 Z-S01 至 Z-S09，不更新本地 Schema 镜像",
		"authorization_mode":       "本机固定管理员身份 + 显式授权 + 绑定当前线上结构的 preview_id",
		"preview_id":               plan.PreviewID,
		"enterprise_wecom_updated": false,
	}
}
