package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

// Registry metadata must never use the instance's business lookup key.
const registrySelfKey = "smart_sheets_ids_registry_v1"

// These are the complete, observed platform defaults, not a title-only deletion
// rule. Any customized property or unknown column fails before the first write.
var registryDefaultFields = map[string]map[string]any{
	"文本": {"field_title": "文本", "field_type": "FIELD_TYPE_TEXT"},
	"单选": {"field_title": "单选", "field_type": "FIELD_TYPE_SINGLE_SELECT", "property_single_select": map[string]any{"is_multiple": false, "is_quick_add": true, "options": []any{}}},
	"人员": {"field_title": "人员", "field_type": "FIELD_TYPE_USER", "property_user": map[string]any{"is_multiple": true, "is_notified": true}},
	"数字": {"field_title": "数字", "field_type": "FIELD_TYPE_NUMBER", "property_number": map[string]any{"decimal_places": 1, "use_separate": true}},
	"日期": {"field_title": "日期", "field_type": "FIELD_TYPE_DATE_TIME", "property_date_time": map[string]any{"auto_fill": false, "format": "yyyy\"年\"m\"月\"d\"日\""}},
}

type registrySelfJournal struct {
	DocumentID     string            `json:"document_id"`
	SheetID        string            `json:"sheet_id"`
	TenantRoute    string            `json:"tenant_route"`
	OperatorDigest string            `json:"operator_digest"`
	Values         map[string]string `json:"values"`
	Pending        bool              `json:"pending"`
	RecordID       string            `json:"record_id,omitempty"`
}

func registryRawFields(ctx context.Context, client wecomRequester, doc, sheet string) ([]any, error) {
	r, e := client.Request(ctx, "get_fields", map[string]any{"docid": doc, "sheet_id": sheet})
	if e != nil || apiError(r) != nil {
		return nil, fmt.Errorf("Registry 字段读取失败")
	}
	fields := resultSlice(r, "fields")
	if len(fields) == 0 {
		return nil, fmt.Errorf("Registry 字段为空")
	}
	titles, ids := map[string]bool{}, map[string]bool{}
	for _, raw := range fields {
		f, ok := raw.(map[string]any)
		title, _ := f["field_title"].(string)
		id, _ := f["field_id"].(string)
		typ, _ := f["field_type"].(string)
		if !ok || title == "" || !initializeIdentifier.MatchString(id) || typ == "" || titles[title] || ids[id] {
			return nil, fmt.Errorf("Registry 字段格式或唯一性无效")
		}
		titles[title], ids[id] = true, true
	}
	return fields, nil
}

func registryReadRows(ctx context.Context, client wecomRequester, doc, sheet string) ([]any, error) {
	rows, complete, err := readAllInitializeRecords(ctx, client, doc, sheet)
	if err != nil || !complete {
		return nil, fmt.Errorf("Registry 记录完整分页未通过")
	}
	seen := map[string]bool{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		id, _ := r["record_id"].(string)
		_, valuesOK := r["values"].(map[string]any)
		if !ok || !valuesOK || !initializeIdentifier.MatchString(id) || seen[id] {
			return nil, fmt.Errorf("Registry 记录格式或唯一性无效")
		}
		seen[id] = true
	}
	return rows, nil
}

// Called only with durable proof that this initialization created the document.
// Reuse the original primary field; never attempt to delete it.
func normalizeOwnedRegistryDefaults(ctx context.Context, client wecomRequester, doc, sheet, backupPath string) error {
	fields, err := registryRawFields(ctx, client, doc, sheet)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, title := range registryBootstrapFields {
		expected[title] = true
	}
	var primary string
	remove := []string{}
	duplicateKey := ""
	extras := 0
	for i, raw := range fields {
		f := raw.(map[string]any)
		title := f["field_title"].(string)
		id := f["field_id"].(string)
		if expected[title] {
			if f["field_type"] != "FIELD_TYPE_TEXT" {
				return fmt.Errorf("Registry 标准字段类型冲突")
			}
			if title == "registry_key" {
				duplicateKey = id
			}
			continue
		}
		template, known := registryDefaultFields[title]
		shape := map[string]any{}
		for k, v := range f {
			if k != "field_id" {
				shape[k] = v
			}
		}
		if !known || digestValue(shape) != digestValue(template) {
			return fmt.Errorf("Registry 包含未知或已修改字段，拒绝自动清理")
		}
		extras++
		if title == "文本" {
			if i != 0 {
				return fmt.Errorf("默认主字段位置不可核验")
			}
			primary = id
		} else {
			remove = append(remove, id)
		}
	}
	if extras == 0 {
		return nil
	}
	rows, err := registryReadRows(ctx, client, doc, sheet)
	if err != nil {
		return err
	}
	if len(rows) > 5 {
		return fmt.Errorf("默认记录数量不匹配，拒绝清理")
	}
	for _, raw := range rows {
		if !initializeRecordValuesEmpty(raw.(map[string]any)["values"].(map[string]any)) {
			return fmt.Errorf("Registry 存在非空记录，拒绝清理默认模板")
		}
	}
	// Durable preimage before any deletion. A rerun never overwrites its evidence.
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		raw, _ := json.Marshal(map[string]any{"document_id": doc, "sheet_id": sheet, "fields": fields, "records": rows})
		fd, e := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = fd.Write(raw)
		if e == nil {
			e = fd.Sync()
		}
		closeErr := fd.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if err != nil {
		return err
	}
	// Repeat the bounded empty-value check immediately before destructive writes.
	fresh, err := registryReadRows(ctx, client, doc, sheet)
	if err != nil || digestValue(fresh) != digestValue(rows) {
		return fmt.Errorf("Registry 清理前记录发生变化")
	}
	if len(rows) > 0 {
		ids := []string{}
		for _, r := range rows {
			ids = append(ids, r.(map[string]any)["record_id"].(string))
		}
		response, e := client.Request(ctx, "delete_records", map[string]any{"docid": doc, "sheet_id": sheet, "record_ids": ids})
		after, readErr := registryReadRows(ctx, client, doc, sheet)
		if readErr != nil || len(after) != 0 {
			return fmt.Errorf("默认空记录清理尚未回读确认；不继续修改字段")
		}
		if e != nil || apiError(response) != nil { /* exact readback proves the deletion */
		}
	}
	if primary != "" && duplicateKey != "" {
		remove = append(remove, duplicateKey)
	}
	if len(remove) > 0 {
		freshFields, e := registryRawFields(ctx, client, doc, sheet)
		if e != nil || digestValue(freshFields) != digestValue(fields) {
			return fmt.Errorf("Registry 清理前字段发生变化")
		}
		remaining, e := registryReadRows(ctx, client, doc, sheet)
		if e != nil || len(remaining) != 0 {
			return fmt.Errorf("Registry 清理前出现记录")
		}
		_, _ = client.Request(ctx, "delete_fields", map[string]any{"docid": doc, "sheet_id": sheet, "field_ids": remove})
		after, e := registryRawFields(ctx, client, doc, sheet)
		if e != nil {
			return e
		}
		for _, f := range after {
			for _, id := range remove {
				if f.(map[string]any)["field_id"] == id {
					return fmt.Errorf("默认字段删除尚未回读确认")
				}
			}
		}
	}
	if primary != "" {
		_, _ = client.Request(ctx, "update_fields", map[string]any{"docid": doc, "sheet_id": sheet, "fields": []any{map[string]any{"field_id": primary, "field_title": "registry_key", "field_type": "FIELD_TYPE_TEXT"}}})
		after, e := registryRawFields(ctx, client, doc, sheet)
		if e != nil {
			return e
		}
		found := false
		for _, raw := range after {
			f := raw.(map[string]any)
			if f["field_id"] == primary && f["field_title"] == "registry_key" && f["field_type"] == "FIELD_TYPE_TEXT" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("默认主字段复用尚未回读确认")
		}
	}
	return nil
}

func registrySelfValues(runtime config.Config, doc, url, createdAt, fingerprint string) map[string]string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if createdAt == "" {
		createdAt = now
	}
	return map[string]string{"registry_key": registrySelfKey, "name": "SMART_SHEETS_IDS", "type": "smart_sheet", "doc_type": "10", "docid": doc, "url": url, "mcp_source": runtime.TenantRoute, "business_domain": "registry", "document_role": "registry", "lifecycle_status": "active", "schema_version": "1", "schema_fingerprint": fingerprint, "created_at": createdAt, "created_by": runtime.WecomOperatorUserID, "last_verified_at": now, "last_change_at": now, "last_change_by": runtime.WecomOperatorUserID, "last_change_reason": "initialize registry self-registration", "registry_revision": "1", "notes": "SMART_SHEETS_IDS 自登记；仅描述索引表自身，不作为业务文档路由。"}
}

func inspectRegistrySelf(rows []any, fields map[string]config.Field, runtime config.Config, doc, fingerprint string) (string, error) {
	if runtime.RegistryKey == registrySelfKey {
		return "", fmt.Errorf("业务 registry_key 与保留自登记键冲突")
	}
	matches := 0
	id := ""
	for _, raw := range rows {
		record := raw.(map[string]any)
		values := record["values"].(map[string]any)
		get := func(title string) string { return initializeTextCell(values[fields[title].ID]) }
		if get("registry_key") != registrySelfKey && get("docid") != doc {
			continue
		}
		matches++
		id, _ = record["record_id"].(string)
		required := map[string]string{"registry_key": registrySelfKey, "docid": doc, "name": "SMART_SHEETS_IDS", "document_role": "registry", "lifecycle_status": "active", "type": "smart_sheet", "doc_type": "10", "schema_version": "1", "schema_fingerprint": fingerprint, "mcp_source": runtime.TenantRoute, "registry_revision": "1", "business_domain": "registry"}
		for title, value := range required {
			if get(title) != value {
				return "", fmt.Errorf("Registry 自登记内容冲突")
			}
		}
		parsedURL, urlErr := url.Parse(get("url"))
		if urlErr != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
			return "", fmt.Errorf("Registry 自登记URL无效")
		}
		for _, title := range []string{"created_at", "last_verified_at", "last_change_at"} {
			if _, err := time.Parse(time.RFC3339Nano, get(title)); err != nil {
				return "", fmt.Errorf("Registry 自登记时间无效")
			}
		}
		for _, title := range registryBootstrapFields {
			if get(title) == "" {
				return "", fmt.Errorf("Registry 自登记元数据缺失")
			}
		}
	}
	if matches > 1 {
		return "", fmt.Errorf("Registry 自登记记录不唯一")
	}
	return id, nil
}

// Check every persisted value, including metadata that cannot be inferred from
// schema alone. Imported rows without a journal retain their original creator.
func verifyRegistrySelfEvidence(rows []any, fields map[string]config.Field, runtime config.Config, doc, sheet, id, shareURL, createdAt string) error {
	data, err := os.ReadFile(runtime.StatePath + ".registry-self.json")
	expected := map[string]string{}
	if err == nil {
		var journal registrySelfJournal
		if json.Unmarshal(data, &journal) != nil || journal.DocumentID != doc || journal.SheetID != sheet || journal.TenantRoute != runtime.TenantRoute || journal.OperatorDigest != digestValue(runtime.WecomOperatorUserID) || (journal.RecordID != "" && journal.RecordID != id) || len(journal.Values) != len(registryBootstrapFields) {
			return fmt.Errorf("Registry 自登记回执身份或字段不匹配")
		}
		for _, title := range registryBootstrapFields {
			value, ok := journal.Values[title]
			if !ok || value == "" {
				return fmt.Errorf("Registry 自登记回执字段不完整")
			}
			expected[title] = value
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for title, value := range map[string]string{"url": shareURL, "created_at": createdAt} {
		if value == "" {
			continue
		}
		if previous, ok := expected[title]; ok && previous != value {
			return fmt.Errorf("Registry 自登记创建证据冲突")
		}
		expected[title] = value
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["record_id"] != id {
			continue
		}
		values := row["values"].(map[string]any)
		for title, value := range expected {
			if initializeTextCell(values[fields[title].ID]) != value {
				return fmt.Errorf("Registry 自登记字段 %s 与创建回执不符", title)
			}
		}
		return nil
	}
	return fmt.Errorf("Registry 自登记回执对应记录缺失")
}

func registryExactSchema(raw []any) (map[string]config.Field, error) {
	if len(raw) != len(registryBootstrapFields) {
		return nil, fmt.Errorf("Registry 必须恰好包含20个标准字段")
	}
	fields := map[string]config.Field{}
	for _, item := range raw {
		f := item.(map[string]any)
		title := f["field_title"].(string)
		fields[title] = config.Field{Title: title, ID: f["field_id"].(string), Type: f["field_type"].(string)}
	}
	for _, title := range registryBootstrapFields {
		if fields[title].Type != "FIELD_TYPE_TEXT" {
			return nil, fmt.Errorf("Registry 标准字段缺失或类型冲突")
		}
	}
	return fields, nil
}

func ensureRegistrySelf(ctx context.Context, runtime config.Config, client wecomRequester, doc, sheet, shareURL, createdAt string) error {
	raw, err := registryRawFields(ctx, client, doc, sheet)
	if err != nil {
		return err
	}
	fields, err := registryExactSchema(raw)
	if err != nil {
		return err
	}
	fingerprint := schemaFingerprint(raw)
	rows, err := registryReadRows(ctx, client, doc, sheet)
	if err != nil {
		return err
	}
	for _, raw := range rows {
		if initializeRecordValuesEmpty(raw.(map[string]any)["values"].(map[string]any)) {
			return fmt.Errorf("Registry 存在未清理空记录，禁止报告初始化完成")
		}
	}
	id, err := inspectRegistrySelf(rows, fields, runtime, doc, fingerprint)
	if err != nil {
		return err
	}
	path := runtime.StatePath + ".registry-self.json"
	journal := registrySelfJournal{}
	data, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if exists {
		if json.Unmarshal(data, &journal) != nil || journal.DocumentID != doc || journal.SheetID != sheet || journal.TenantRoute != runtime.TenantRoute || journal.OperatorDigest != digestValue(runtime.WecomOperatorUserID) {
			return fmt.Errorf("Registry 自登记 journal 身份不匹配")
		}
	}
	if id != "" {
		if err := verifyRegistrySelfEvidence(rows, fields, runtime, doc, sheet, id, shareURL, createdAt); err != nil {
			return err
		}
		if journal.RecordID != "" && journal.RecordID != id {
			return fmt.Errorf("Registry 自登记记录与回执不符")
		}
		if exists {
			journal.Pending = false
			journal.RecordID = id
			return saveRegistryCompletionJSON(path, journal)
		}
		return nil
	}
	if exists && (journal.Pending || journal.RecordID != "") {
		return fmt.Errorf("Registry 自登记写入结果仍不确定，禁止重复新增")
	}
	if shareURL == "" {
		return fmt.Errorf("Registry 自登记缺少真实文档URL")
	}
	journal = registrySelfJournal{DocumentID: doc, SheetID: sheet, TenantRoute: runtime.TenantRoute, OperatorDigest: digestValue(runtime.WecomOperatorUserID), Values: registrySelfValues(runtime, doc, shareURL, createdAt, fingerprint), Pending: true}
	if err := saveRegistryCompletionJSON(path, journal); err != nil {
		return err
	}
	values := map[string]any{}
	for title, value := range journal.Values {
		values[fields[title].ID] = []any{map[string]any{"type": "text", "text": value}}
	}
	response, writeErr := client.Request(ctx, "add_records", map[string]any{"docid": doc, "sheet_id": sheet, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []any{map[string]any{"values": values}}})
	journal.RecordID = initializeCreatedRecordID(response)
	if err := saveRegistryCompletionJSON(path, journal); err != nil {
		return err
	}
	verifiedFields, fieldErr := registryRawFields(ctx, client, doc, sheet)
	if fieldErr != nil || schemaFingerprint(verifiedFields) != fingerprint {
		return fmt.Errorf("Registry 自登记写后字段发生变化")
	}
	rows, err = registryReadRows(ctx, client, doc, sheet)
	if err != nil {
		return err
	}
	id, err = inspectRegistrySelf(rows, fields, runtime, doc, fingerprint)
	if err != nil || id == "" || journal.RecordID != "" && journal.RecordID != id {
		return fmt.Errorf("Registry 自登记写后回读未确认；保留哨兵，禁止重复新增")
	}
	if err := verifyRegistrySelfEvidence(rows, fields, runtime, doc, sheet, id, shareURL, createdAt); err != nil {
		return err
	}
	if writeErr != nil || apiError(response) != nil { /* readback recovers a lost response */
	}
	journal.Pending = false
	journal.RecordID = id
	return saveRegistryCompletionJSON(path, journal)
}

func saveRegistryCompletionJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return saveRegistryJSONBytes(path, data)
}
