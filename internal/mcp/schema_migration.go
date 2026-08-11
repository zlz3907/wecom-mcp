package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/user"
	"sort"
	"strings"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

const (
	subjectMigrationID      = "zoop_subject_v1"
	subjectLinksMigrationID = "zoop_subject_links_v1"
	subjectRole             = "Z-S09"
	subjectSheetTitle       = "Z-S09｜协作主体"
	schemaMigrationGroup    = "schema_migration"
	schemaAdminPermission   = "apply_approved_schema_migration_as_admin"
)

var currentSchemaAdminUser = func() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", err
	}
	return account.Username, nil
}

type schemaMigrationField struct {
	Title    string
	Type     string
	Property map[string]any
}

type schemaMigrationPlan struct {
	MigrationID     string
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

func schemaMigrationTools() []tool {
	migrationIDs := []string{subjectMigrationID, subjectLinksMigrationID}
	return []tool{
		{"wecom_schema_migration_preview", "只读生成当前固定 Zoop 文档的管理员 Schema 增量迁移预览。仅支持内置迁移目录，不接受文档、子表或字段 ID。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"migration_id"}, "properties": map[string]any{"migration_id": map[string]any{"type": "string", "enum": migrationIDs}}}},
		{"wecom_schema_migration_apply", "执行已预览的内置 Zoop Schema 增量迁移。必须同时通过本机管理员身份、显式管理员授权和未过期预览校验；仅允许新增或配置本迁移刚创建的结构，完成后回读。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"migration_id", "preview_id", "admin_authorization"}, "properties": map[string]any{"migration_id": map[string]any{"type": "string", "enum": migrationIDs}, "preview_id": map[string]any{"type": "string", "minLength": 64, "maxLength": 64}, "admin_authorization": map[string]any{"const": schemaAdminPermission}}}},
	}
}

func (s *Server) previewSchemaMigration(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input struct {
		MigrationID string `json:"migration_id"`
	}
	if err := strictDecode(raw, &input, "migration_id"); err != nil {
		return nil, err
	}
	if input.MigrationID == subjectLinksMigrationID {
		plan, err := buildSubjectLinksMigrationPlan(ctx, runtime, client)
		if err != nil {
			return nil, err
		}
		return publicSubjectLinksMigrationPlan(plan), nil
	}
	plan, err := buildSchemaMigrationPlan(ctx, runtime, client, input.MigrationID)
	if err != nil {
		return nil, err
	}
	return publicSchemaMigrationPlan(plan), nil
}

func (s *Server) applySchemaMigration(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input struct {
		MigrationID        string `json:"migration_id"`
		PreviewID          string `json:"preview_id"`
		AdminAuthorization string `json:"admin_authorization"`
	}
	if err := strictDecode(raw, &input, "migration_id", "preview_id", "admin_authorization"); err != nil {
		return nil, err
	}
	if input.AdminAuthorization != schemaAdminPermission {
		return nil, fmt.Errorf("缺少明确的管理员 Schema 迁移授权")
	}
	if err := verifySchemaAdmin(runtime); err != nil {
		return nil, err
	}
	if input.MigrationID == subjectLinksMigrationID {
		return s.applySubjectLinksMigration(ctx, runtime, client, input.PreviewID)
	}
	plan, err := buildSchemaMigrationPlan(ctx, runtime, client, input.MigrationID)
	if err != nil {
		return nil, err
	}
	if input.PreviewID != plan.PreviewID {
		return nil, fmt.Errorf("Schema 迁移预览已失效，请重新预览当前线上结构")
	}
	if len(plan.Operations) == 0 {
		result := publicSchemaMigrationPlan(plan)
		result["state"] = "already_applied"
		result["readback_verified"] = true
		return result, nil
	}
	reservationKey := "schema-migration:" + input.MigrationID + ":" + input.PreviewID
	if err := s.reserve(runtime.StatePath, reservationKey, input.PreviewID); err != nil {
		return nil, err
	}

	sheetID := plan.SheetID
	if !plan.SheetExists {
		result, err := client.Request(ctx, "add_sheet", map[string]any{"docid": plan.DocumentID, "properties": map[string]any{"title": subjectSheetTitle}})
		if err != nil {
			return nil, err
		}
		if err := apiError(result); err != nil {
			return nil, fmt.Errorf("创建协作主体子表未确认成功: %w", err)
		}
		sheetID, err = resolveExactSheetID(ctx, client, plan.DocumentID, subjectSheetTitle)
		if err != nil {
			return map[string]any{"state": "applied_readback_pending", "migration_id": input.MigrationID, "preview_id": input.PreviewID, "readback_verified": false}, nil
		}
	}

	fields, err := readMigrationFields(ctx, client, plan.DocumentID, sheetID)
	if err != nil {
		return map[string]any{"state": "applied_readback_pending", "migration_id": input.MigrationID, "preview_id": input.PreviewID, "readback_verified": false}, nil
	}
	expected, err := subjectMigrationFieldsFromOnlineContext(ctx, runtime, client, plan.DocumentID)
	if err != nil {
		return nil, err
	}
	defaultField, missing, err := assessSubjectFields(ctx, client, plan.DocumentID, sheetID, expected, fields)
	if err != nil {
		return nil, err
	}
	if defaultField != nil {
		fieldID, _ := defaultField["field_id"].(string)
		result, err := client.Request(ctx, "update_fields", map[string]any{"docid": plan.DocumentID, "sheet_id": sheetID, "fields": []any{map[string]any{"field_id": fieldID, "field_title": "主体编号", "field_type": "FIELD_TYPE_TEXT"}}})
		if err != nil {
			return nil, err
		}
		if err := apiError(result); err != nil {
			return nil, fmt.Errorf("配置协作主体主字段未确认成功: %w", err)
		}
		missing = removeMigrationField(missing, "主体编号")
	}
	if len(missing) > 0 {
		wireFields := make([]any, 0, len(missing))
		for _, field := range missing {
			wireFields = append(wireFields, migrationFieldWire(field))
		}
		result, err := client.Request(ctx, "add_fields", map[string]any{"docid": plan.DocumentID, "sheet_id": sheetID, "fields": wireFields})
		if err != nil {
			return nil, err
		}
		if err := apiError(result); err != nil {
			return nil, fmt.Errorf("新增协作主体字段未确认成功: %w", err)
		}
	}

	readbackPlan, err := buildSchemaMigrationPlan(ctx, runtime, client, input.MigrationID)
	if err != nil || len(readbackPlan.Operations) != 0 {
		return map[string]any{"state": "applied_readback_pending", "migration_id": input.MigrationID, "preview_id": input.PreviewID, "readback_verified": false}, nil
	}
	s.complete(runtime.StatePath, reservationKey, input.PreviewID)
	return map[string]any{
		"state":                   "applied",
		"migration_id":            input.MigrationID,
		"preview_id":              input.PreviewID,
		"target_role":             subjectRole,
		"sheet_title":             subjectSheetTitle,
		"field_count":             readbackPlan.CurrentSummary["field_count"],
		"readback_verified":       true,
		"local_mirror_updated":    false,
		"next_required_operation": "Owner 授权后执行线上到本地 Schema 字典同步",
	}, nil
}

func verifySchemaAdmin(runtime config.Config) error {
	if runtime.SchemaAdminUser == "" {
		return fmt.Errorf("实例未配置 schema_admin_user，管理员 Schema 迁移保持关闭")
	}
	current, err := currentSchemaAdminUser()
	if err != nil || current != runtime.SchemaAdminUser {
		return fmt.Errorf("当前本机身份不是已登记的 Schema 管理员")
	}
	return nil
}

func buildSchemaMigrationPlan(ctx context.Context, runtime config.Config, client *wecom.Client, migrationID string) (schemaMigrationPlan, error) {
	if migrationID != subjectMigrationID {
		return schemaMigrationPlan{}, fmt.Errorf("未知或未登记的 Schema 迁移")
	}
	for _, operation := range []string{"get_sheet", "get_fields", "get_records", "add_sheet", "add_fields", "update_fields"} {
		if !runtime.AllowsInGroup(schemaMigrationGroup, operation) {
			return schemaMigrationPlan{}, fmt.Errorf("Schema 迁移白名单未允许 %s", operation)
		}
	}
	s07, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S07", runtime.Allows)
	if err != nil {
		return schemaMigrationPlan{}, err
	}
	s07Fields, err := wecom.ReadFields(ctx, client, s07, runtime.Allows)
	if err != nil {
		return schemaMigrationPlan{}, err
	}
	projectFieldID := ""
	for _, field := range s07Fields {
		if field["field_title"] == "项目编号与名称" && field["field_type"] == "FIELD_TYPE_TEXT" {
			projectFieldID, _ = field["field_id"].(string)
		}
	}
	if projectFieldID == "" {
		return schemaMigrationPlan{}, fmt.Errorf("Z-S07 缺少项目编号与名称主文本字段，拒绝猜测关联目标")
	}

	plan := schemaMigrationPlan{MigrationID: migrationID, DocumentID: s07.DocumentID, CurrentSummary: map[string]any{}, ProposedSummary: map[string]any{"target_role": subjectRole, "sheet_title": subjectSheetTitle, "field_count": 16}}
	sheetID, sheetErr := resolveExactSheetID(ctx, client, s07.DocumentID, subjectSheetTitle)
	if sheetErr != nil {
		if !strings.Contains(sheetErr.Error(), "未找到") {
			return schemaMigrationPlan{}, sheetErr
		}
		plan.State = "ready"
		plan.CurrentSummary = map[string]any{"sheet_exists": false, "field_count": 0}
		plan.MissingFields = subjectMigrationFields(s07.SheetID, projectFieldID)
		plan.Operations = []map[string]any{
			{"operation": "add_sheet", "before": "不存在", "after": subjectSheetTitle},
			{"operation": "update_fields", "before": "新子表默认主字段", "after": "主体编号（文本）"},
			{"operation": "add_fields", "before": 0, "after": 15, "field_titles": migrationFieldTitles(plan.MissingFields[1:])},
		}
		plan.PreviewID = schemaMigrationPreviewID(runtime, plan)
		return plan, nil
	}
	plan.SheetExists, plan.SheetID = true, sheetID
	fields, err := readMigrationFields(ctx, client, s07.DocumentID, sheetID)
	if err != nil {
		return schemaMigrationPlan{}, err
	}
	expected := subjectMigrationFields(s07.SheetID, projectFieldID)
	defaultField, missing, err := assessSubjectFields(ctx, client, s07.DocumentID, sheetID, expected, fields)
	if err != nil {
		return schemaMigrationPlan{}, err
	}
	plan.DefaultField, plan.MissingFields = defaultField, missing
	plan.CurrentSummary = map[string]any{"sheet_exists": true, "sheet_title": subjectSheetTitle, "field_count": len(fields), "field_titles": fieldTitles(fields)}
	if defaultField != nil {
		plan.Operations = append(plan.Operations, map[string]any{"operation": "update_fields", "before": defaultField["field_title"], "after": "主体编号（文本）"})
	}
	fieldsToAdd := missing
	if defaultField != nil {
		fieldsToAdd = removeMigrationField(fieldsToAdd, "主体编号")
	}
	if len(fieldsToAdd) > 0 {
		plan.Operations = append(plan.Operations, map[string]any{"operation": "add_fields", "before": 0, "after": len(fieldsToAdd), "field_titles": migrationFieldTitles(fieldsToAdd)})
	}
	if len(plan.Operations) == 0 {
		plan.State = "up_to_date"
	} else {
		plan.State = "ready"
	}
	plan.PreviewID = schemaMigrationPreviewID(runtime, plan)
	return plan, nil
}

func subjectMigrationFields(projectSheetID, projectFieldID string) []schemaMigrationField {
	selectField := func(title string, options ...string) schemaMigrationField {
		items := make([]any, 0, len(options))
		for index, option := range options {
			items = append(items, map[string]any{"text": option, "style": index%10 + 1})
		}
		return schemaMigrationField{Title: title, Type: "FIELD_TYPE_SINGLE_SELECT", Property: map[string]any{"property_single_select": map[string]any{"is_quick_add": false, "options": items}}}
	}
	return []schemaMigrationField{
		{Title: "主体编号", Type: "FIELD_TYPE_TEXT"},
		{Title: "主体名称", Type: "FIELD_TYPE_TEXT"},
		selectField("主体类型", "人员主体", "AI 执行主体", "系统执行主体"),
		{Title: "企业微信成员或责任人", Type: "FIELD_TYPE_USER", Property: map[string]any{"property_user": map[string]any{"is_multiple": false, "is_notified": false}}},
		selectField("AI 工具平台", "不适用", "Codex", "TRAE Work", "TRAE IDE", "Tencent WorkBuddy", "其他"),
		{Title: "AI 实例ID", Type: "FIELD_TYPE_TEXT"},
		{Title: "参与项目", Type: "FIELD_TYPE_REFERENCE", Property: map[string]any{"property_reference": map[string]any{"sub_id": projectSheetID, "field_id": projectFieldID, "is_multiple": true}}},
		{Title: "能力与职责边界", Type: "FIELD_TYPE_TEXT"},
		selectField("最大自主风险等级", "不得自主", "R0", "R1", "R2", "R3"),
		selectField("执行模式", "人工", "AI 辅助", "AI 自主", "确定性系统执行"),
		selectField("自动化能力状态", "不适用", "待验证", "人工触发", "定时唤醒", "外部调度"),
		selectField("注册来源", "企业微信同步", "AI 自注册", "系统配置", "人工登记"),
		selectField("主体状态", "启用", "暂停", "停用"),
		{Title: "最后观察时间", Type: "FIELD_TYPE_DATE_TIME", Property: map[string]any{"property_date_time": map[string]any{"auto_fill": false, "format": "yyyy-mm-dd hh:mm"}}},
		{Title: "主体幂等键", Type: "FIELD_TYPE_TEXT"},
		{Title: "来源修订", Type: "FIELD_TYPE_TEXT"},
	}
}

func assessSubjectFields(ctx context.Context, client *wecom.Client, documentID, sheetID string, expected []schemaMigrationField, fields []map[string]any) (map[string]any, []schemaMigrationField, error) {
	byTitle := map[string]map[string]any{}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		if title != "" {
			if byTitle[title] != nil {
				return nil, nil, fmt.Errorf("Z-S09 存在重复字段 %s，拒绝迁移", title)
			}
			byTitle[title] = field
		}
	}
	missing := []schemaMigrationField{}
	for _, wanted := range expected {
		current := byTitle[wanted.Title]
		if current == nil {
			missing = append(missing, wanted)
			continue
		}
		if err := compatibleMigrationField(current, wanted); err != nil {
			return nil, nil, err
		}
	}
	if len(fields) == 1 && len(missing) == len(expected) {
		if fields[0]["field_type"] != "FIELD_TYPE_TEXT" {
			return nil, nil, fmt.Errorf("Z-S09 默认主字段不是文本，拒绝自动调整")
		}
		response, err := client.Request(ctx, "get_records", map[string]any{"docid": documentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 1})
		if err != nil || apiError(response) != nil || len(recordsFrom(response)) != 0 {
			return nil, nil, fmt.Errorf("Z-S09 已存在非空或状态不明，拒绝把现有字段视为迁移默认字段")
		}
		return fields[0], missing, nil
	}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		if byTitle[title] != nil && !containsMigrationField(expected, title) {
			return nil, nil, fmt.Errorf("Z-S09 存在迁移目录外字段 %s，拒绝覆盖现有结构", title)
		}
	}
	return nil, missing, nil
}

func subjectMigrationFieldsFromOnlineContext(ctx context.Context, runtime config.Config, client *wecom.Client, documentID string) ([]schemaMigrationField, error) {
	// The S07 target was already verified when the plan was built. Resolve it
	// again here so recovery after a partial apply still compares the exact
	// online reference contract rather than a cached identifier.
	s07, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S07", runtime.Allows)
	if err != nil {
		return nil, err
	}
	if s07.DocumentID != documentID {
		return nil, fmt.Errorf("Z-S07 与 Schema 迁移目标不在同一受管文档")
	}
	fields, err := wecom.ReadFields(ctx, client, s07, runtime.Allows)
	if err != nil {
		return nil, err
	}
	projectFieldID := ""
	for _, field := range fields {
		if field["field_title"] == "项目编号与名称" && field["field_type"] == "FIELD_TYPE_TEXT" {
			projectFieldID, _ = field["field_id"].(string)
		}
	}
	if projectFieldID == "" {
		return nil, fmt.Errorf("Z-S07 缺少项目编号与名称主文本字段，拒绝猜测关联目标")
	}
	return subjectMigrationFields(s07.SheetID, projectFieldID), nil
}

func compatibleMigrationField(current map[string]any, wanted schemaMigrationField) error {
	if current["field_type"] != wanted.Type {
		return fmt.Errorf("Z-S09 字段 %s 类型不兼容：线上 %v，迁移要求 %s", wanted.Title, current["field_type"], wanted.Type)
	}
	if wanted.Type == "FIELD_TYPE_SINGLE_SELECT" {
		wantedOptions := selectOptionLabels(wanted.Property["property_single_select"])
		currentOptions := selectOptionLabels(current["property_single_select"])
		if strings.Join(wantedOptions, "\x00") != strings.Join(currentOptions, "\x00") {
			return fmt.Errorf("Z-S09 字段 %s 选项不兼容", wanted.Title)
		}
	}
	if wanted.Type == "FIELD_TYPE_REFERENCE" {
		wantedProperty, _ := wanted.Property["property_reference"].(map[string]any)
		currentProperty := referenceProperty(current)
		for _, key := range []string{"sub_id", "field_id", "is_multiple"} {
			if fmt.Sprint(currentProperty[key]) != fmt.Sprint(wantedProperty[key]) {
				return fmt.Errorf("Z-S09 字段 %s 的跨表关联目标不兼容", wanted.Title)
			}
		}
	}
	return nil
}

func selectOptionLabels(value any) []string {
	property, _ := value.(map[string]any)
	items, _ := property["options"].([]any)
	labels := make([]string, 0, len(items))
	for _, item := range items {
		option, _ := item.(map[string]any)
		label, _ := option["text"].(string)
		if label == "" {
			label, _ = option["name"].(string)
		}
		if label != "" {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	return labels
}

func resolveExactSheetID(ctx context.Context, client *wecom.Client, documentID, title string) (string, error) {
	response, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil {
		return "", err
	}
	if err := apiError(response); err != nil {
		return "", err
	}
	result, _ := response["result"].(map[string]any)
	items, _ := result["sheet_list"].([]any)
	matches := []string{}
	for _, item := range items {
		sheet, _ := item.(map[string]any)
		name := ""
		for _, key := range []string{"name", "sheet_name", "title"} {
			if value, ok := sheet[key].(string); ok && value != "" {
				name = value
				break
			}
		}
		if name == title {
			if id, ok := sheet["sheet_id"].(string); ok && id != "" {
				matches = append(matches, id)
			}
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

func readMigrationFields(ctx context.Context, client *wecom.Client, documentID, sheetID string) ([]map[string]any, error) {
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	result, _ := response["result"].(map[string]any)
	items, _ := result["fields"].([]any)
	fields := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if field, ok := item.(map[string]any); ok {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("目标子表未返回任何字段")
	}
	return fields, nil
}

func migrationFieldWire(field schemaMigrationField) map[string]any {
	result := map[string]any{"field_title": field.Title, "field_type": field.Type}
	for key, value := range field.Property {
		result[key] = value
	}
	return result
}

func removeMigrationField(fields []schemaMigrationField, title string) []schemaMigrationField {
	result := make([]schemaMigrationField, 0, len(fields))
	for _, field := range fields {
		if field.Title != title {
			result = append(result, field)
		}
	}
	return result
}

func containsMigrationField(fields []schemaMigrationField, title string) bool {
	for _, field := range fields {
		if field.Title == title {
			return true
		}
	}
	return false
}

func migrationFieldTitles(fields []schemaMigrationField) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		result = append(result, field.Title)
	}
	return result
}

func fieldTitles(fields []map[string]any) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if title, ok := field["field_title"].(string); ok {
			result = append(result, title)
		}
	}
	sort.Strings(result)
	return result
}

func schemaMigrationPreviewID(runtime config.Config, plan schemaMigrationPlan) string {
	data, _ := json.Marshal(map[string]any{
		"migration_id":  plan.MigrationID,
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

func publicSchemaMigrationPlan(plan schemaMigrationPlan) map[string]any {
	return map[string]any{
		"state":                    plan.State,
		"migration_id":             plan.MigrationID,
		"target_role":              subjectRole,
		"sheet_title":              subjectSheetTitle,
		"current":                  plan.CurrentSummary,
		"proposed":                 plan.ProposedSummary,
		"operations":               plan.Operations,
		"impact":                   "仅新增 Z-S09 协作主体及其目录字段；不删除记录，不改现有 Z-S01 至 Z-S08 结构，不更新本地 Schema 镜像",
		"authorization_mode":       "本机固定管理员身份 + 显式授权 + 绑定当前线上结构的 preview_id",
		"preview_id":               plan.PreviewID,
		"enterprise_wecom_updated": false,
	}
}
