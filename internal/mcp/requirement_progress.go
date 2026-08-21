package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

var requirementProgressFields = []string{
	"计划任务基线",
	"当前任务总数",
	"已完成任务数",
	"阻塞任务数",
}

type requirementProgress struct {
	Current   int
	Completed int
	Blocked   int
}

func (s *Server) reconcileRequirementProgress(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input struct {
		IdempotencyKey string `json:"idempotency_key"`
		SourceRevision string `json:"source_revision"`
	}
	if err := strictDecode(raw, &input, "idempotency_key", "source_revision"); err != nil {
		return nil, err
	}
	if len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 || len(input.SourceRevision) == 0 || len(input.SourceRevision) > 256 {
		return nil, fmt.Errorf("idempotency_key 或 source_revision 无效")
	}
	for _, operation := range []string{"get_records", "update_records"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("实例白名单未允许 %s", operation)
		}
	}
	requirementField, requirementOK := schema.Roles["Z-S03"]["主需求"]
	statusField, statusOK := schema.Roles["Z-S03"]["任务状态"]
	if !requirementOK || requirementField.Type != "FIELD_TYPE_REFERENCE" || !statusOK || statusField.Type != "FIELD_TYPE_SINGLE_SELECT" {
		return nil, fmt.Errorf("Schema 缺少 S03 进度重算契约")
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	releaseProgressLock, err := acquireProgressFileLock(ctx, runtime.StatePath)
	if err != nil {
		return nil, err
	}
	defer releaseProgressLock()
	targets, err := wecom.ResolveTargets(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, []string{"Z-S01", "Z-S03"}, runtime.Allows)
	if err != nil {
		return nil, err
	}
	requirements, err := client.Request(ctx, "get_records", map[string]any{"docid": targets["Z-S01"].DocumentID, "sheet_id": targets["Z-S01"].SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200})
	if err != nil || apiError(requirements) != nil || recordSetMayBeIncomplete(requirements, 200) {
		return nil, fmt.Errorf("无法取得完整 S01 快照，拒绝重算")
	}
	tasks, err := client.Request(ctx, "get_records", map[string]any{"docid": targets["Z-S03"].DocumentID, "sheet_id": targets["Z-S03"].SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200})
	if err != nil || apiError(tasks) != nil || recordSetMayBeIncomplete(tasks, 200) {
		return nil, fmt.Errorf("无法取得完整 S03 快照，拒绝重算")
	}
	affected := map[string]struct{}{}
	for _, item := range recordsFrom(requirements) {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if recordID, ok := record["record_id"].(string); ok && recordID != "" {
			affected[recordID] = struct{}{}
		}
	}
	if len(affected) == 0 {
		return map[string]any{"state": "up_to_date", "requirement_count": 0, "readback_verified": true}, nil
	}
	digestData, _ := json.Marshal(map[string]any{"operation": "reconcile_requirement_progress", "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision})
	digestSum := sha256.Sum256(digestData)
	digest := hex.EncodeToString(digestSum[:])
	if err := s.reserve(runtime.StatePath, input.IdempotencyKey, digest); err != nil {
		return nil, err
	}
	result, err := applyRequirementProgress(ctx, runtime, schema, client, targets["Z-S03"], affected, tasks, requirementField.ID, statusField.ID)
	if err != nil {
		return map[string]any{"state": "progress_reconcile_pending", "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false, "progress_error": err.Error()}, nil
	}
	if err := s.completeState(runtime.StatePath, input.IdempotencyKey, digest); err != nil {
		return map[string]any{"state": "reconciled_idempotency_completion_pending", "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": true, "idempotency_error": err.Error(), "result": result}, nil
	}
	return map[string]any{"state": "reconciled", "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": true, "result": result}, nil
}

// withRequirementProgressDefaults makes a newly created requirement observable
// before Planner creates its first task. A requirement cannot start with task
// counts because task creation is a separately verified S03 mutation.
func withRequirementProgressDefaults(schema config.Schema, input applyInput) (applyInput, error) {
	if input.TargetRole != "Z-S01" || input.Operation != "add_records" {
		return input, nil
	}
	for _, title := range requirementProgressFields {
		field, exists := schema.Roles["Z-S01"][title]
		if !exists || field.Type != "FIELD_TYPE_NUMBER" {
			return input, fmt.Errorf("Schema 缺少 S01 零值初始化字段 %s", title)
		}
	}
	for index := range input.Records {
		values := make(map[string]any, len(input.Records[index].Values)+len(requirementProgressFields))
		for title, value := range input.Records[index].Values {
			values[title] = value
		}
		for _, title := range requirementProgressFields {
			if provided, exists := values[title]; exists {
				number, numeric := provided.(float64)
				if !numeric || number != 0 {
					return input, fmt.Errorf("新建 S01 时 %s 必须为 0，任务计数只能由后续 S03 事件产生", title)
				}
			}
			values[title] = float64(0)
		}
		input.Records[index].Values = values
	}
	return input, nil
}

func (s *Server) syncRequirementProgress(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, taskTarget wecom.Target, input applyInput, writeResult, before, after map[string]any) (map[string]any, error) {
	if input.TargetRole != "Z-S03" {
		return nil, nil
	}
	if recordSetMayBeIncomplete(after, 200) {
		return nil, fmt.Errorf("任务记录超过单次完整读取边界，拒绝写入可能失真的需求进度")
	}
	changedTaskIDs := taskRecordIDs(input, writeResult)
	if len(changedTaskIDs) != len(input.Records) {
		return nil, fmt.Errorf("无法确定全部变更任务的 record_id")
	}
	requirementField, ok := schema.Roles["Z-S03"]["主需求"]
	if !ok || requirementField.Type != "FIELD_TYPE_REFERENCE" {
		return nil, fmt.Errorf("Schema 缺少 Z-S03 主需求关联契约")
	}
	statusField, ok := schema.Roles["Z-S03"]["任务状态"]
	if !ok || statusField.Type != "FIELD_TYPE_SINGLE_SELECT" {
		return nil, fmt.Errorf("Schema 缺少 Z-S03 任务状态契约")
	}
	affected := map[string]struct{}{}
	collectAffectedRequirements(affected, changedTaskIDs, before, requirementField.ID)
	collectAffectedRequirements(affected, changedTaskIDs, after, requirementField.ID)
	if len(affected) == 0 {
		return nil, fmt.Errorf("变更任务没有可回读的主需求，无法同步进度")
	}
	return applyRequirementProgress(ctx, runtime, schema, client, taskTarget, affected, after, requirementField.ID, statusField.ID)
}

func applyRequirementProgress(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, taskTarget wecom.Target, affected map[string]struct{}, taskSnapshot map[string]any, requirementFieldID, statusFieldID string) (map[string]any, error) {
	requirementTarget, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S01", runtime.Allows)
	if err != nil {
		return nil, err
	}
	for attempt := 1; attempt <= 3; attempt++ {
		counts := summarizeRequirementProgress(recordsFrom(taskSnapshot), requirementFieldID, statusFieldID)
		updates, summary, err := requirementProgressUpdates(schema, affected, counts)
		if err != nil {
			return nil, err
		}
		result, err := client.Request(ctx, "update_records", map[string]any{
			"docid": requirementTarget.DocumentID, "sheet_id": requirementTarget.SheetID,
			"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": updates,
		})
		if err != nil {
			return nil, fmt.Errorf("需求进度写入失败: %w", err)
		}
		if err := apiError(result); err != nil {
			return nil, fmt.Errorf("任务已写入，但需求进度未确认成功: %w", err)
		}
		readback, err := client.Request(ctx, "get_records", map[string]any{
			"docid": requirementTarget.DocumentID, "sheet_id": requirementTarget.SheetID,
			"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200,
		})
		if err != nil || apiError(readback) != nil || !verifyReadback("update_records", updates, result, readback) {
			return nil, fmt.Errorf("任务已写入，但需求进度回读未通过")
		}
		latest, err := client.Request(ctx, "get_records", map[string]any{
			"docid": taskTarget.DocumentID, "sheet_id": taskTarget.SheetID,
			"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200,
		})
		if err != nil || apiError(latest) != nil || recordSetMayBeIncomplete(latest, 200) {
			return nil, fmt.Errorf("需求进度写入后无法取得完整任务快照")
		}
		latestCounts := summarizeRequirementProgress(recordsFrom(latest), requirementFieldID, statusFieldID)
		if sameRequirementProgress(affected, counts, latestCounts) {
			return map[string]any{"state": "applied", "readback_verified": true, "requirements": summary, "reconciliation_attempts": attempt}, nil
		}
		taskSnapshot = latest
	}
	return nil, fmt.Errorf("连续三次复核期间任务仍在变化，需求进度进入待恢复状态")
}

func sameRequirementProgress(affected map[string]struct{}, left, right map[string]requirementProgress) bool {
	for requirementID := range affected {
		if left[requirementID] != right[requirementID] {
			return false
		}
	}
	return true
}

func taskRecordIDs(input applyInput, writeResult map[string]any) []string {
	if input.Operation == "add_records" {
		return writeRecordIDs(writeResult)
	}
	ids := make([]string, 0, len(input.Records))
	for _, record := range input.Records {
		if record.RecordID != "" {
			ids = append(ids, record.RecordID)
		}
	}
	return ids
}

func collectAffectedRequirements(target map[string]struct{}, taskIDs []string, response map[string]any, fieldID string) {
	if response == nil {
		return
	}
	wanted := map[string]struct{}{}
	for _, id := range taskIDs {
		wanted[id] = struct{}{}
	}
	for _, item := range recordsFrom(response) {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		recordID, _ := record["record_id"].(string)
		if _, changed := wanted[recordID]; !changed {
			continue
		}
		values, _ := record["values"].(map[string]any)
		for _, requirementID := range cellRecordIDs(values[fieldID]) {
			target[requirementID] = struct{}{}
		}
	}
}

func summarizeRequirementProgress(records []any, requirementFieldID, statusFieldID string) map[string]requirementProgress {
	result := map[string]requirementProgress{}
	for _, item := range records {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		values, _ := record["values"].(map[string]any)
		status := cellText(values[statusFieldID])
		for _, requirementID := range cellRecordIDs(values[requirementFieldID]) {
			progress := result[requirementID]
			if status != "已取消" {
				progress.Current++
			}
			if status == "已完成" {
				progress.Completed++
			}
			if status == "阻塞" {
				progress.Blocked++
			}
			result[requirementID] = progress
		}
	}
	return result
}

func requirementProgressUpdates(schema config.Schema, affected map[string]struct{}, counts map[string]requirementProgress) ([]map[string]any, []map[string]any, error) {
	fields := schema.Roles["Z-S01"]
	current, currentOK := fields["当前任务总数"]
	completed, completedOK := fields["已完成任务数"]
	blocked, blockedOK := fields["阻塞任务数"]
	if !currentOK || !completedOK || !blockedOK || current.Type != "FIELD_TYPE_NUMBER" || completed.Type != "FIELD_TYPE_NUMBER" || blocked.Type != "FIELD_TYPE_NUMBER" {
		return nil, nil, fmt.Errorf("Schema 缺少 S01 任务进度数字段契约")
	}
	ids := make([]string, 0, len(affected))
	for id := range affected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	updates := make([]map[string]any, 0, len(ids))
	summary := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		progress := counts[id]
		updates = append(updates, map[string]any{"record_id": id, "values": map[string]any{
			current.ID: float64(progress.Current), completed.ID: float64(progress.Completed), blocked.ID: float64(progress.Blocked),
		}})
		summary = append(summary, map[string]any{"record_id": id, "current": progress.Current, "completed": progress.Completed, "blocked": progress.Blocked})
	}
	return updates, summary, nil
}

func cellRecordIDs(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := []string{}
	for _, item := range items {
		switch entry := item.(type) {
		case string:
			if entry != "" {
				result = append(result, entry)
			}
		case map[string]any:
			if id, ok := entry["record_id"].(string); ok && id != "" {
				result = append(result, id)
			}
		}
	}
	return result
}

func recordSetMayBeIncomplete(response map[string]any, limit int) bool {
	result, _ := response["result"].(map[string]any)
	if more, _ := result["has_more"].(bool); more {
		return true
	}
	if cursor, _ := result["next_cursor"].(string); cursor != "" {
		return true
	}
	return len(recordsFrom(response)) >= limit
}

func acquireProgressFileLock(ctx context.Context, statePath string) (func(), error) {
	lockPath := statePath + ".progress.lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("创建进度锁目录失败")
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开进度锁失败")
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("获取进度锁失败")
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("等待进度锁超时")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func acquireStateFileLock(statePath string) (func(), error) {
	lockPath := statePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
