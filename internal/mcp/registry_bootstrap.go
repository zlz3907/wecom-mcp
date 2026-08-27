package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type wecomRequester interface {
	Request(context.Context, string, any) (map[string]any, error)
}

type registryBootstrapState struct {
	Phase      string `json:"phase"`
	DocumentID string `json:"document_id,omitempty"`
	SheetID    string `json:"sheet_id,omitempty"`
	ShareURL   string `json:"share_url,omitempty"`
	StartedAt  string `json:"started_at"`
	UpdatedAt  string `json:"updated_at"`
}

var registryBootstrapFields = []string{
	"business_domain", "created_at", "created_by", "doc_type", "docid", "document_role",
	"last_change_at", "last_change_by", "last_change_reason", "last_verified_at",
	"lifecycle_status", "mcp_source", "name", "notes", "registry_key", "registry_revision",
	"schema_fingerprint", "schema_version", "type", "url",
}

func registryBootstrapStatePath(runtime config.Config) string {
	return runtime.StatePath + ".registry-bootstrap.json"
}

func (s *Server) bootstrapRegistry(ctx context.Context, runtime config.Config, client wecomRequester, raw json.RawMessage) (any, error) {
	prelockInstanceName, prelockTenantRoute := runtime.InstanceName, runtime.TenantRoute
	var input struct {
		OwnerAuthorization string `json:"owner_authorization"`
	}
	if err := strictDecode(raw, &input, "owner_authorization"); err != nil {
		return nil, err
	}
	if input.OwnerAuthorization != "create_and_persist_default_registry" {
		return nil, fmt.Errorf("仅接受 Owner 的 SMART_SHEETS_IDS 缺省初始化授权")
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(instanceLifecycleLockPath(runtime))
	if err != nil {
		return nil, fmt.Errorf("获取实例生命周期锁失败")
	}
	defer release()
	if s.store == nil {
		return nil, fmt.Errorf("实例配置 store 不可用")
	}
	runtime, err = s.store.BootstrapCandidate()
	if err != nil {
		return nil, fmt.Errorf("锁内重新加载实例配置失败")
	}
	if runtime.InstanceName != prelockInstanceName || runtime.TenantRoute != prelockTenantRoute {
		return nil, fmt.Errorf("锁内实例身份或 tenant_route 已变化；禁止使用锁外客户端写入")
	}
	if runtime.RegistryDocumentID != "" {
		return map[string]any{
			"state": "already_configured", "created": false, "config_updated": false,
			"instance_name": runtime.InstanceName,
		}, nil
	}
	for _, operation := range []string{"create_smartsheet", "get_sheet", "get_fields", "add_fields"} {
		if !runtime.Allows(operation) {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 缺省初始化需要白名单允许 %s", operation)
		}
	}
	if client == nil {
		return nil, fmt.Errorf("企业微信客户端未初始化")
	}

	statePath := registryBootstrapStatePath(runtime)
	state, exists, err := loadRegistryBootstrapState(statePath)
	if err != nil {
		return nil, err
	}
	createdNow := false
	if !exists {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		state = registryBootstrapState{Phase: "creating", StartedAt: now, UpdatedAt: now}
		if err := reserveRegistryBootstrapState(statePath, state); err != nil {
			return nil, err
		}
		created, err := client.Request(ctx, "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "SMART_SHEETS_IDS"})
		if err != nil {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 创建结果不确定；本地哨兵已保留，禁止自动重试: %w", err)
		}
		if err := apiError(created); err != nil {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 创建未成功；本地哨兵已保留，禁止自动重试: %w", err)
		}
		documentID, shareURL, err := createdDocumentIdentity(created)
		if err != nil {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 创建回执不完整；本地哨兵已保留，禁止自动重试: %w", err)
		}
		state.Phase, state.DocumentID, state.ShareURL = "created", documentID, shareURL
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveRegistryBootstrapState(statePath, state); err != nil {
			return nil, fmt.Errorf("SMART_SHEETS_IDS 已创建但本地状态写入失败；禁止自动重试: %w", err)
		}
		createdNow = true
	} else if state.Phase == "creating" && state.DocumentID == "" {
		return nil, fmt.Errorf("检测到未完成的 SMART_SHEETS_IDS 创建哨兵且没有可核验文档 ID；禁止自动重试")
	}
	if state.DocumentID == "" {
		return nil, fmt.Errorf("SMART_SHEETS_IDS bootstrap 状态缺少文档 ID")
	}

	sheetID, fieldCount, err := ensureRegistryFields(ctx, client, state.DocumentID)
	if err != nil {
		return nil, fmt.Errorf("SMART_SHEETS_IDS 已创建但字段核验未完成；将从同一文档恢复，禁止新建第二张: %w", err)
	}
	state.Phase, state.SheetID, state.UpdatedAt = "schema_verified", sheetID, time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveRegistryBootstrapState(statePath, state); err != nil {
		return nil, err
	}
	if err := s.store.PersistRegistryDocumentID(state.DocumentID); err != nil {
		return nil, fmt.Errorf("SMART_SHEETS_IDS 字段已核验但配置写回失败；禁止新建第二张: %w", err)
	}
	persisted, err := s.store.Current()
	if err != nil || persisted.RegistryDocumentID != state.DocumentID {
		return nil, fmt.Errorf("registry_document_id 写回后未通过配置回读")
	}
	verifiedSheetID, verifiedCount, err := ensureRegistryFields(ctx, client, persisted.RegistryDocumentID)
	if err != nil || verifiedSheetID != sheetID || verifiedCount != fieldCount {
		return nil, fmt.Errorf("registry_document_id 写回后线上字段回读未通过")
	}
	state.Phase, state.UpdatedAt = "verified", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveRegistryBootstrapState(statePath, state); err != nil {
		return nil, err
	}
	return map[string]any{
		"state": "created_configured_readback_verified", "created": createdNow,
		"config_updated": true, "readback_verified": true, "registry_field_count": verifiedCount,
		"instance_name": persisted.InstanceName,
	}, nil
}

func ensureRegistryFields(ctx context.Context, client wecomRequester, documentID string) (string, int, error) {
	sheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil {
		return "", 0, err
	}
	if err := apiError(sheets); err != nil {
		return "", 0, err
	}
	sheetID := firstSmartSheetID(sheets)
	if sheetID == "" {
		return "", 0, fmt.Errorf("新建 SMART_SHEETS_IDS 未返回默认智能子表")
	}
	fields, err := registryFieldDefinitions(ctx, client, documentID, sheetID)
	if err != nil {
		return "", 0, err
	}
	for _, title := range registryBootstrapFields {
		if field, ok := fields[title]; ok {
			if field.Type != "FIELD_TYPE_TEXT" {
				return "", 0, fmt.Errorf("字段 %s 已存在但不是文本类型", title)
			}
			continue
		}
		response, err := client.Request(ctx, "add_fields", map[string]any{
			"docid": documentID, "sheet_id": sheetID,
			"fields": []map[string]any{{"field_title": title, "field_type": "FIELD_TYPE_TEXT"}},
		})
		if err != nil {
			return "", 0, err
		}
		if err := apiError(response); err != nil {
			return "", 0, err
		}
	}
	verified, err := registryFieldDefinitions(ctx, client, documentID, sheetID)
	if err != nil {
		return "", 0, err
	}
	for _, title := range registryBootstrapFields {
		field, ok := verified[title]
		if !ok || field.Type != "FIELD_TYPE_TEXT" {
			return "", 0, fmt.Errorf("字段 %s 新增后未通过回读", title)
		}
	}
	return sheetID, len(registryBootstrapFields), nil
}

func registryFieldDefinitions(ctx context.Context, client wecomRequester, documentID, sheetID string) (map[string]config.Field, error) {
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
	if err != nil {
		return nil, err
	}
	if err := apiError(response); err != nil {
		return nil, err
	}
	result := map[string]config.Field{}
	counts := map[string]int{}
	for _, item := range resultSlice(response, "fields") {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, _ := field["field_title"].(string)
		id, _ := field["field_id"].(string)
		fieldType, _ := field["field_type"].(string)
		if title == "" || id == "" || fieldType == "" {
			continue
		}
		counts[title]++
		result[title] = config.Field{Title: title, ID: id, Type: fieldType}
	}
	for title, count := range counts {
		if count != 1 {
			return nil, fmt.Errorf("字段 %s 不唯一", title)
		}
	}
	return result, nil
}

func loadRegistryBootstrapState(path string) (registryBootstrapState, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return registryBootstrapState{}, false, nil
	}
	if err != nil {
		return registryBootstrapState{}, false, err
	}
	var state registryBootstrapState
	if err := json.Unmarshal(data, &state); err != nil || state.Phase == "" || state.StartedAt == "" {
		return registryBootstrapState{}, false, fmt.Errorf("SMART_SHEETS_IDS bootstrap 本地状态无效；拒绝自动创建")
	}
	return state, true, nil
}

func reserveRegistryBootstrapState(path string, state registryBootstrapState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(state)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("创建 SMART_SHEETS_IDS bootstrap 哨兵失败: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func saveRegistryBootstrapState(path string, state registryBootstrapState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".registry-bootstrap-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
