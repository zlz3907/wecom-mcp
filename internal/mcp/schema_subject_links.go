package mcp

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

type subjectLinkSpec struct {
	Role       string
	Title      string
	Type       string
	IsMultiple bool
}

type subjectLinksMigrationPlan struct {
	State           string
	DocumentID      string
	SubjectSheetID  string
	SubjectFieldID  string
	SheetIDs        map[string]string
	MissingByRole   map[string][]schemaMigrationField
	Operations      []map[string]any
	CurrentSummary  map[string]any
	ProposedSummary map[string]any
	PreviewID       string
}

func subjectLinkMigrationSpecs() []subjectLinkSpec {
	return []subjectLinkSpec{
		{Role: "Z-S01", Title: "需求提出主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S02", Title: "决策主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S03", Title: "任务责任主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S03", Title: "任务执行主体", Type: "FIELD_TYPE_REFERENCE", IsMultiple: true},
		{Role: "Z-S04", Title: "操作主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S04", Title: "来源会话ID", Type: "FIELD_TYPE_TEXT"},
		{Role: "Z-S05", Title: "调度主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S06", Title: "发起主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S06", Title: "执行主体", Type: "FIELD_TYPE_REFERENCE"},
		{Role: "Z-S06", Title: "会话全局键", Type: "FIELD_TYPE_TEXT"},
		{Role: "Z-S07", Title: "参与主体", Type: "FIELD_TYPE_REFERENCE", IsMultiple: true},
	}
}

func subjectLinkRoles() []string {
	return []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", subjectRole}
}

func buildSubjectLinksMigrationPlan(ctx context.Context, runtime config.Config, client *wecom.Client) (subjectLinksMigrationPlan, error) {
	for _, operation := range []string{"get_sheet", "get_fields", "add_fields"} {
		if !runtime.AllowsInGroup(schemaMigrationGroup, operation) {
			return subjectLinksMigrationPlan{}, fmt.Errorf("Schema 迁移白名单未允许 %s", operation)
		}
	}
	targets, err := wecom.ResolveTargets(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, subjectLinkRoles(), runtime.Allows)
	if err != nil {
		return subjectLinksMigrationPlan{}, err
	}
	subjectTarget := targets[subjectRole]
	allFields, err := readSubjectLinkTargetFields(ctx, client, targets, runtime.Allows)
	if err != nil {
		return subjectLinksMigrationPlan{}, err
	}
	subjectFields := allFields[subjectRole]
	subjectFieldID, err := uniqueTextFieldID(subjectRole, subjectFields, "主体编号")
	if err != nil {
		return subjectLinksMigrationPlan{}, err
	}

	plan := subjectLinksMigrationPlan{
		State:          "ready",
		DocumentID:     subjectTarget.DocumentID,
		SubjectSheetID: subjectTarget.SheetID,
		SubjectFieldID: subjectFieldID,
		SheetIDs:       map[string]string{},
		MissingByRole:  map[string][]schemaMigrationField{},
		CurrentSummary: map[string]any{},
		ProposedSummary: map[string]any{
			"target_role":         subjectRole,
			"target_field":        "主体编号",
			"managed_field_count": len(subjectLinkMigrationSpecs()),
		},
	}
	for role, target := range targets {
		plan.SheetIDs[role] = target.SheetID
	}

	for _, role := range subjectLinkRoles()[:len(subjectLinkRoles())-1] {
		fields := allFields[role]
		missing, present, err := assessSubjectLinkFields(role, fields, subjectTarget.SheetID, subjectFieldID)
		if err != nil {
			return subjectLinksMigrationPlan{}, err
		}
		plan.CurrentSummary[role] = map[string]any{
			"field_count":     len(fields),
			"managed_present": present,
			"managed_missing": migrationFieldTitles(missing),
		}
		if len(missing) == 0 {
			continue
		}
		plan.MissingByRole[role] = missing
		plan.Operations = append(plan.Operations, map[string]any{
			"operation":   "add_fields",
			"target_role": role,
			"before":      "缺少已登记主体字段",
			"after":       migrationFieldTitles(missing),
			"field_count": len(missing),
		})
	}
	if len(plan.Operations) == 0 {
		plan.State = "up_to_date"
	}
	plan.PreviewID = schemaMigrationPreviewID(runtime, schemaMigrationPlan{
		MigrationID:     subjectLinksMigrationID,
		DocumentID:      plan.DocumentID,
		SheetID:         plan.SubjectSheetID,
		CurrentSummary:  plan.CurrentSummary,
		ProposedSummary: plan.ProposedSummary,
		Operations:      plan.Operations,
	})
	return plan, nil
}

func readSubjectLinkTargetFields(ctx context.Context, client *wecom.Client, targets map[string]wecom.Target, allowed func(string) bool) (map[string][]map[string]any, error) {
	fieldsByRole := map[string][]map[string]any{}
	errorsByRole := map[string]error{}
	var mu sync.Mutex
	var wait sync.WaitGroup
	for _, role := range subjectLinkRoles() {
		role := role
		wait.Add(1)
		go func() {
			defer wait.Done()
			fields, err := wecom.ReadFields(ctx, client, targets[role], allowed)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errorsByRole[role] = err
				return
			}
			fieldsByRole[role] = fields
		}()
	}
	wait.Wait()
	for _, role := range subjectLinkRoles() {
		if err := errorsByRole[role]; err != nil {
			return nil, fmt.Errorf("读取 %s 字段失败: %w", role, err)
		}
	}
	return fieldsByRole, nil
}

func uniqueTextFieldID(role string, fields []map[string]any, title string) (string, error) {
	matches := []map[string]any{}
	for _, field := range fields {
		if field["field_title"] == title {
			matches = append(matches, field)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("%s 字段 %s 未找到或不唯一", role, title)
	}
	if matches[0]["field_type"] != "FIELD_TYPE_TEXT" {
		return "", fmt.Errorf("%s 字段 %s 不是文本字段，拒绝猜测关联目标", role, title)
	}
	fieldID, _ := matches[0]["field_id"].(string)
	if fieldID == "" {
		return "", fmt.Errorf("%s 字段 %s 缺少 field_id", role, title)
	}
	return fieldID, nil
}

func assessSubjectLinkFields(role string, fields []map[string]any, subjectSheetID, subjectFieldID string) ([]schemaMigrationField, int, error) {
	byTitle := map[string][]map[string]any{}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		if title != "" {
			byTitle[title] = append(byTitle[title], field)
		}
	}
	missing := []schemaMigrationField{}
	present := 0
	for _, spec := range subjectLinkMigrationSpecs() {
		if spec.Role != role {
			continue
		}
		wanted := subjectLinkMigrationField(spec, subjectSheetID, subjectFieldID)
		matches := byTitle[spec.Title]
		if len(matches) == 0 {
			missing = append(missing, wanted)
			continue
		}
		if len(matches) != 1 {
			return nil, 0, fmt.Errorf("%s 存在重复字段 %s，拒绝迁移", role, spec.Title)
		}
		if err := compatibleSubjectLinkField(role, matches[0], wanted); err != nil {
			return nil, 0, err
		}
		present++
	}
	return missing, present, nil
}

func subjectLinkMigrationField(spec subjectLinkSpec, subjectSheetID, subjectFieldID string) schemaMigrationField {
	field := schemaMigrationField{Title: spec.Title, Type: spec.Type}
	if spec.Type == "FIELD_TYPE_REFERENCE" {
		field.Property = map[string]any{"property_reference": map[string]any{
			"sub_id":      subjectSheetID,
			"field_id":    subjectFieldID,
			"is_multiple": spec.IsMultiple,
		}}
	}
	return field
}

func compatibleSubjectLinkField(role string, current map[string]any, wanted schemaMigrationField) error {
	if current["field_type"] != wanted.Type {
		return fmt.Errorf("%s 字段 %s 类型不兼容：线上 %v，迁移要求 %s", role, wanted.Title, current["field_type"], wanted.Type)
	}
	if wanted.Type != "FIELD_TYPE_REFERENCE" {
		return nil
	}
	wantedProperty, _ := wanted.Property["property_reference"].(map[string]any)
	currentProperty := referenceProperty(current)
	for _, key := range []string{"sub_id", "field_id", "is_multiple"} {
		if fmt.Sprint(currentProperty[key]) != fmt.Sprint(wantedProperty[key]) {
			return fmt.Errorf("%s 字段 %s 的跨表关联目标或多选配置不兼容", role, wanted.Title)
		}
	}
	return nil
}

func publicSubjectLinksMigrationPlan(plan subjectLinksMigrationPlan) map[string]any {
	return map[string]any{
		"state":                    plan.State,
		"migration_id":             subjectLinksMigrationID,
		"target_roles":             subjectLinkRoles()[:len(subjectLinkRoles())-1],
		"reference_target":         map[string]any{"role": subjectRole, "field_title": "主体编号"},
		"current":                  plan.CurrentSummary,
		"proposed":                 plan.ProposedSummary,
		"operations":               plan.Operations,
		"impact":                   "仅给 Z-S01 至 Z-S07 新增已登记的主体关联或会话标识字段；不删除或改型，不回填历史记录，不修改 Z-S08、Z-S09 和本地 Schema 镜像",
		"authorization_mode":       "本机固定管理员身份 + 显式授权 + 绑定当前线上结构的 preview_id",
		"preview_id":               plan.PreviewID,
		"enterprise_wecom_updated": false,
	}
}

func (s *Server) applySubjectLinksMigration(ctx context.Context, runtime config.Config, client *wecom.Client, previewID string) (any, error) {
	plan, err := buildSubjectLinksMigrationPlan(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	if previewID != plan.PreviewID {
		return nil, fmt.Errorf("Schema 迁移预览已失效，请重新预览当前线上结构")
	}
	if len(plan.Operations) == 0 {
		result := publicSubjectLinksMigrationPlan(plan)
		result["state"] = "already_applied"
		result["readback_verified"] = true
		return result, nil
	}
	reservationKey := "schema-migration:" + subjectLinksMigrationID + ":" + previewID
	if err := s.reserveWithOperator(runtime.StatePath, reservationKey, previewID, runtime.WecomOperatorUserID); err != nil {
		return nil, err
	}

	roles := make([]string, 0, len(plan.MissingByRole))
	for role := range plan.MissingByRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	errorsByRole := map[string]error{}
	var mu sync.Mutex
	var wait sync.WaitGroup
	for _, role := range roles {
		role := role
		wait.Add(1)
		go func() {
			defer wait.Done()
			wireFields := make([]any, 0, len(plan.MissingByRole[role]))
			for _, field := range plan.MissingByRole[role] {
				wireFields = append(wireFields, migrationFieldWire(field))
			}
			result, err := client.Request(ctx, "add_fields", map[string]any{
				"docid":    plan.DocumentID,
				"sheet_id": plan.SheetIDs[role],
				"fields":   wireFields,
			})
			if err == nil {
				err = apiError(result)
			}
			if err != nil {
				mu.Lock()
				errorsByRole[role] = err
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	for _, role := range roles {
		if err := errorsByRole[role]; err != nil {
			return nil, fmt.Errorf("%s 新增主体字段未确认成功: %w", role, err)
		}
	}

	readbackPlan, err := buildSubjectLinksMigrationPlan(ctx, runtime, client)
	if err != nil || len(readbackPlan.Operations) != 0 {
		return withOperatorAudit(map[string]any{
			"state":             "applied_readback_pending",
			"migration_id":      subjectLinksMigrationID,
			"preview_id":        previewID,
			"readback_verified": false,
		}, runtime.WecomOperatorUserID), nil
	}
	if err := s.completeStateWithOperator(runtime.StatePath, reservationKey, previewID, runtime.WecomOperatorUserID); err != nil {
		return withOperatorAudit(map[string]any{"state": "applied_idempotency_completion_pending", "migration_id": subjectLinksMigrationID, "preview_id": previewID, "readback_verified": true, "idempotency_error": err.Error()}, runtime.WecomOperatorUserID), nil
	}
	return withOperatorAudit(map[string]any{
		"state":                   "applied",
		"migration_id":            subjectLinksMigrationID,
		"preview_id":              previewID,
		"target_roles":            subjectLinkRoles()[:len(subjectLinkRoles())-1],
		"managed_field_count":     len(subjectLinkMigrationSpecs()),
		"readback_verified":       true,
		"local_mirror_updated":    false,
		"next_required_operation": "Owner 授权后执行线上到本地 Schema 字典同步",
	}, runtime.WecomOperatorUserID), nil
}
