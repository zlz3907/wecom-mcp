// Package mcp implements the small stdio JSON-RPC surface for one instance.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/zoopschema"
)

var validRoles = map[string]struct{}{"Z-S01": {}, "Z-S02": {}, "Z-S03": {}, "Z-S04": {}, "Z-S05": {}, "Z-S06": {}, "Z-S07": {}, "Z-S08": {}, "Z-S09": {}}

type Server struct {
	store               *config.Store
	stateMu             sync.Mutex
	progressMu          sync.Mutex
	previewMu           sync.Mutex
	previews            map[string]initializePreview
	initializeCatalog   func() (zoopschema.Catalog, error)
	initializeLocalUser func() (string, error)
}

func New(configPath string) *Server { return &Server{store: config.NewStore(configPath)} }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

var tools = []tool{
	{"wecom_instance_initialize", "首次初始化统一入口。action=status 执行只读 dry-run；action=apply 复用同一工具提交未过期 preview。生产 catalog 未达到 complete_for_creation 时，fresh 创建继续失败关闭。", instanceInitializeFacadeToolSchema()},
	{"wecom_instance_initialize_status", "只读观察当前固定租户的 Registry、九张 Zoop 表、本地 Schema 与初始化 journal，返回可审计 dry-run 和短期 preview_id；不修改线上或本地状态。恢复 ID 仅可绑定已有 uncertain sentinel，不是任意导入入口。", instanceInitializeStatusToolSchema()},
	{"wecom_instance_initialize_apply", "校验短期 dry-run 与固定 Owner 授权，按 durable journal 幂等协调 Registry、Zoop 九表、唯一 active row、Schema generation、配置备份与 Z-S01 只读 smoke；不清理导入文档的既有内容，创建结果不确定时失败关闭。", instanceInitializeApplyToolSchema()},
	{"wecom_registry_bootstrap", "仅在当前固定租户实例缺少 registry_document_id 时，显式创建 SMART_SHEETS_IDS、建立标准文本字段、回读核验并原子写回本地配置。创建前写入本地哨兵；状态不确定时失败关闭且不会重复创建。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"owner_authorization"}, "properties": map[string]any{"owner_authorization": map[string]any{"const": "create_and_persist_default_registry"}}}},
	{"wecom_schema_status", "读取当前本地 Schema 镜像状态。不会读取或修改企业微信线上字段。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_schema_probe", "只读回查当前固定租户的九张 Zoop 表线上字段，并与本地机器 Schema 镜像比较。不会写入企业微信或更新本地镜像。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_schema_sync", "仅在 Owner 明确授权后，从当前固定企业微信实例只读回查九张 Zoop 表字段，并覆盖本地机器可读 Schema 镜像。不会修改企业微信数据或结构。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"owner_authorization"}, "properties": map[string]any{"owner_authorization": map[string]any{"const": "online_to_local_schema_dictionary"}}}},
	{"wecom_field_codec_lab_create", "创建或返回当前固定租户专用的企业微信字段编码验证表。创建后必须写入 SMART_SHEETS_IDS 并回读核验；登记失败即停止使用，不会创建第二张表。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_read", "仅在验证表已登记为 active 后，只读返回字段定义与填写样本原始值，用于固化经过人工填写验证的写入 codec。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_reference_debug", "对已登记验证表的关联字段执行带与不带 key_type 的只读对照，用于核对企业微信关联值返回形态。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_reference_write_probe", "仅在已登记字段验证表创建独立关联目标与来源记录，将受控 {record_id} 输入编译为企业微信实际可落库的 record_id 字符串数组，再回读核验；不修改人工样本行或 Zoop 正式八表。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_write_probe", "仅在已登记的字段编码验证表中写入一条文本和复选框探针，并立即回读核验；不修改 Zoop 正式八表。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_replay_probe", "将已由人工填写并回读的可写字段原始值复制为一条新记录，再更新该新记录并回读核验；不修改人工样本行或 Zoop 正式八表。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_registry_status", "读取当前字段编码验证表在 SMART_SHEETS_IDS 中的登记状态与线上登记表字段，不修改任何企业微信数据。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_field_codec_lab_register", "将已创建的字段编码验证表按固定 SMART_SHEETS_IDS 规范登记为 active；仅允许当前实例创建的实验表，登记后回读核验。", map[string]any{"type": "object", "additionalProperties": false}},
	{"wecom_api_call", "调用当前固定租户的旧 MCP 全量企业微信 API 契约。operation 必须在实例 API 白名单内；不会接受租户、地址或凭据路由字段。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"operation", "payload"}, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": legacyOperations()}, "payload": map[string]any{"type": "object"}}}},
	{"wecom_record_read", "从当前固定企业微信实例的指定 Zoop 表读取记录。调用方不能指定租户、文档或子表标识。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target_role"}, "properties": map[string]any{"target_role": map[string]any{"type": "string", "enum": []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", "Z-S08", "Z-S09"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}}},
	{"wecom_record_query", "固定租户的只读精确查询：支持 record_id、受控字段过滤、排序、offset 分页、字段投影和紧凑结果。调用方不能指定租户、文档或子表标识。", recordQueryToolSchema()},
	{"wecom_record_apply", "向当前固定企业微信实例的 Zoop 表写入已由字段验证表证明的字段类型，并完成一次回读。新建 S01 自动将四个任务计数字段初始化为 0；S03 新增或更新回读成功后自动重算关联 S01 的当前、完成和阻塞任务数。字段、租户、文档和子表均不可由调用方指定；附件和系统自动字段仍会拒绝写入。关联字段对调用方只接受受控 {record_id} 对象数组，发送企业微信前编译为已验证的 record_id 字符串数组。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target_role", "operation", "idempotency_key", "source_revision", "records"}, "properties": map[string]any{"target_role": map[string]any{"type": "string", "enum": []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", "Z-S08", "Z-S09"}}, "operation": map[string]any{"type": "string", "enum": []string{"add_records", "update_records"}}, "idempotency_key": map[string]any{"type": "string", "minLength": 16, "maxLength": 256}, "source_revision": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "records": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": map[string]any{"type": "object"}}}}},
	{"wecom_requirement_progress_reconcile", "只读取完整 S01/S03 快照并重算所有需求的当前、完成和阻塞任务数；不改计划任务基线，不重复写任务。用于 applied_progress_sync_pending 或巡检发现计数漂移后的受控恢复。", map[string]any{"type": "object", "additionalProperties": false, "required": []string{"idempotency_key", "source_revision"}, "properties": map[string]any{"idempotency_key": map[string]any{"type": "string", "minLength": 16, "maxLength": 256}, "source_revision": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}}}},
}

func init() {
	tools = append(tools, schemaMigrationTools()...)
}

func failure(id any, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}
func success(id any, value any) response { return response{JSONRPC: "2.0", ID: id, Result: value} }
func toolResult(value any) map[string]any {
	data, _ := json.Marshal(value)
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}, "structuredContent": value}
}

func (s *Server) Handle(ctx context.Context, raw []byte) *response {
	var input request
	if err := json.Unmarshal(raw, &input); err != nil || input.JSONRPC != "2.0" || input.Method == "" {
		result := failure(nil, -32600, "Invalid Request")
		return &result
	}
	if input.Method == "notifications/initialized" {
		return nil
	}
	if input.Method == "ping" {
		result := success(input.ID, map[string]any{})
		return &result
	}
	if input.Method == "initialize" {
		result := success(input.ID, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "wecom-mcp-v2", "version": "2.0.0"}})
		return &result
	}
	if input.Method == "tools/list" {
		result := success(input.ID, map[string]any{"tools": tools})
		return &result
	}
	if input.Method != "tools/call" {
		result := failure(input.ID, -32601, "Method not found")
		return &result
	}
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(input.Params, &call); err != nil {
		result := failure(input.ID, -32602, "工具参数无效")
		return &result
	}
	value, err := s.call(ctx, call.Name, call.Arguments)
	if err != nil {
		result := success(input.ID, map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}})
		return &result
	}
	result := success(input.ID, toolResult(value))
	return &result
}

func (s *Server) runtimeClient() (config.Config, *wecom.Client, error) {
	runtime, err := s.store.Current()
	if err != nil {
		return config.Config{}, nil, err
	}
	client, err := wecom.NewFromEnvironment(runtime.TenantRoute)
	if err != nil {
		return config.Config{}, nil, err
	}
	return runtime, client, nil
}

func role(value string) error {
	if _, ok := validRoles[value]; !ok {
		return fmt.Errorf("target_role 必须是 Z-S01 至 Z-S09")
	}
	return nil
}

func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name == "wecom_instance_initialize" || name == "wecom_instance_initialize_status" || name == "wecom_instance_initialize_apply" {
		runtime, err := s.store.BootstrapCandidate()
		if err != nil {
			return nil, err
		}
		client, clientErr := wecom.NewFromEnvironment(runtime.TenantRoute)
		if name == "wecom_instance_initialize" {
			return s.instanceInitializeFacade(ctx, runtime, client, clientErr, raw)
		}
		if name == "wecom_instance_initialize_status" {
			return s.instanceInitializeStatus(ctx, runtime, client, clientErr, raw)
		}
		return s.instanceInitializeApply(ctx, runtime, client, clientErr, raw)
	}
	if name == "wecom_registry_bootstrap" {
		runtime, err := s.store.BootstrapCandidate()
		if err != nil {
			return nil, err
		}
		if runtime.RegistryDocumentID != "" {
			return s.bootstrapRegistry(ctx, runtime, nil, raw)
		}
		client, err := wecom.NewFromEnvironment(runtime.TenantRoute)
		if err != nil {
			return nil, err
		}
		return s.bootstrapRegistry(ctx, runtime, client, raw)
	}
	runtime, client, err := s.runtimeClient()
	if err != nil {
		return nil, err
	}
	if name == "wecom_schema_sync" {
		return s.syncSchema(ctx, runtime, client, raw)
	}
	if name == "wecom_schema_probe" {
		return s.probeSchema(ctx, runtime, client, raw)
	}
	if name == "wecom_schema_migration_preview" {
		return s.previewSchemaMigration(ctx, runtime, client, raw)
	}
	if name == "wecom_schema_migration_apply" {
		return s.applySchemaMigration(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_create" {
		return s.createFieldCodecLab(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_read" {
		return s.readFieldCodecLab(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_reference_debug" {
		return s.debugFieldCodecLabReference(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_reference_write_probe" {
		return s.writeFieldCodecLabReferenceProbe(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_write_probe" {
		return s.writeFieldCodecLabProbe(ctx, runtime, client, raw)
	}
	if name == "wecom_field_codec_lab_replay_probe" {
		return s.replayFieldCodecLabProbe(ctx, runtime, client, raw)
	}
	if name == "wecom_api_call" {
		return s.genericAPICall(ctx, runtime, client, raw)
	}
	schema, err := config.LoadSchema(runtime.SchemaMirrorPath)
	if err != nil {
		return nil, err
	}
	switch name {
	case "wecom_schema_status":
		if err := empty(raw); err != nil {
			return nil, err
		}
		roles := make([]map[string]any, 0, len(schema.Roles))
		for roleName, fields := range schema.Roles {
			roles = append(roles, map[string]any{"target_role": roleName, "field_count": len(fields)})
		}
		sort.Slice(roles, func(i, j int) bool { return roles[i]["target_role"].(string) < roles[j]["target_role"].(string) })
		return map[string]any{"instance_name": runtime.InstanceName, "registry_key": runtime.RegistryKey, "schema_digest": schema.Digest, "config_digest": runtime.Digest(), "roles": roles, "schema_is_local_read_only_mirror": true}, nil
	case "wecom_record_read":
		if !runtime.Allows("get_records") {
			return nil, fmt.Errorf("实例白名单未允许 get_records")
		}
		var input struct {
			TargetRole string `json:"target_role"`
			Limit      int    `json:"limit"`
		}
		if err := strictDecode(raw, &input, "target_role", "limit"); err != nil {
			return nil, err
		}
		if err := role(input.TargetRole); err != nil {
			return nil, err
		}
		if input.Limit == 0 {
			input.Limit = 50
		}
		if input.Limit < 1 || input.Limit > 200 {
			return nil, fmt.Errorf("limit 必须介于 1 和 200")
		}
		target, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, input.TargetRole, runtime.Allows)
		if err != nil {
			return nil, err
		}
		result, err := client.Request(ctx, "get_records", map[string]any{"docid": target.DocumentID, "sheet_id": target.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": input.Limit})
		if err != nil {
			return nil, err
		}
		return sanitizedReadResult(result, input.TargetRole), nil
	case "wecom_record_query":
		return s.queryRecords(ctx, runtime, schema, client, raw)
	case "wecom_record_apply":
		return s.apply(ctx, runtime, schema, client, raw)
	case "wecom_requirement_progress_reconcile":
		return s.reconcileRequirementProgress(ctx, runtime, schema, client, raw)
	case "wecom_field_codec_lab_registry_status":
		return s.fieldCodecLabRegistryStatus(ctx, runtime, client, raw)
	case "wecom_field_codec_lab_register":
		return s.registerFieldCodecLab(ctx, runtime, client, raw)
	default:
		return nil, fmt.Errorf("未知工具: %s", name)
	}
}

func (s *Server) syncSchema(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	var input struct {
		OwnerAuthorization string `json:"owner_authorization"`
	}
	if err := strictDecode(raw, &input, "owner_authorization"); err != nil {
		return nil, err
	}
	if input.OwnerAuthorization != "online_to_local_schema_dictionary" {
		return nil, fmt.Errorf("仅接受 Owner 的线上到本地 Schema 同步授权")
	}
	fieldsByRole, err := readOnlineSchemaFields(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	capturedAt := time.Now().UTC().Format(time.RFC3339)
	if err := config.WriteOnlineMirror(runtime.SchemaMirrorPath, fieldsByRole, capturedAt); err != nil {
		return nil, fmt.Errorf("写入本地 Schema 镜像失败: %w", err)
	}
	loaded, err := config.LoadSchema(runtime.SchemaMirrorPath)
	if err != nil {
		return nil, fmt.Errorf("本地 Schema 镜像回读失败: %w", err)
	}
	counts := map[string]int{}
	for roleName, fields := range loaded.Roles {
		counts[roleName] = len(fields)
	}
	result := map[string]any{"state": "synced", "instance_name": runtime.InstanceName, "schema_digest": loaded.Digest, "captured_at": capturedAt, "field_counts": counts, "online_read_only": true, "local_mirror_updated": true, "readable_mirror_updated": false}
	if filepath.Ext(runtime.SchemaMirrorPath) == ".json" {
		readablePath := strings.TrimSuffix(runtime.SchemaMirrorPath, ".json") + ".md"
		if err := config.WriteReadableOnlineMirror(readablePath, fieldsByRole, capturedAt); err != nil {
			result["state"] = "synced_readable_pending"
			result["readable_mirror_error"] = err.Error()
			return result, nil
		}
		result["readable_mirror_updated"] = true
	}
	return result, nil
}

func readOnlineSchemaFields(ctx context.Context, runtime config.Config, client *wecom.Client) (map[string][]config.Field, error) {
	fieldsByRole := map[string][]config.Field{}
	roles := make([]string, 0, len(validRoles))
	for value := range validRoles {
		roles = append(roles, value)
	}
	sort.Strings(roles)
	targets, err := wecom.ResolveTargets(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, roles, runtime.Allows)
	if err != nil {
		return nil, err
	}
	for _, roleName := range roles {
		rawFields, err := wecom.ReadFields(ctx, client, targets[roleName], runtime.Allows)
		if err != nil {
			return nil, err
		}
		fields, err := mirrorFields(rawFields)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", roleName, err)
		}
		if err := validateUniqueFields(fields); err != nil {
			return nil, fmt.Errorf("%s: %w", roleName, err)
		}
		fieldsByRole[roleName] = fields
	}
	return fieldsByRole, nil
}

func (s *Server) probeSchema(ctx context.Context, runtime config.Config, client *wecom.Client, raw json.RawMessage) (any, error) {
	if err := empty(raw); err != nil {
		return nil, err
	}
	local, err := config.LoadSchema(runtime.SchemaMirrorPath)
	if err != nil {
		return nil, err
	}
	online, err := readOnlineSchemaFields(ctx, runtime, client)
	if err != nil {
		return nil, err
	}
	differences, onlineCounts, localCounts := schemaDifferences(local, online)
	onlineDigest := contractDigest(online)
	localDigest := contractDigest(schemaFieldSlices(local))
	state := schemaProbeState(differences, onlineCounts, localCounts, onlineDigest, localDigest)
	return map[string]any{
		"state":                    state,
		"instance_name":            runtime.InstanceName,
		"checked_at":               time.Now().UTC().Format(time.RFC3339),
		"online_contract_digest":   onlineDigest,
		"local_contract_digest":    localDigest,
		"online_field_counts":      onlineCounts,
		"local_field_counts":       localCounts,
		"differences":              differences,
		"online_read_only":         true,
		"local_mirror_updated":     false,
		"enterprise_wecom_updated": false,
	}, nil
}

func schemaProbeState(differences []map[string]string, onlineCounts, localCounts map[string]int, onlineDigest, localDigest string) string {
	if len(differences) == 0 && reflect.DeepEqual(onlineCounts, localCounts) && onlineDigest == localDigest {
		return "no_drift"
	}
	return "drift_detected"
}

func validateUniqueFields(fields []config.Field) error {
	titles := map[string]struct{}{}
	ids := map[string]struct{}{}
	for _, field := range fields {
		if _, duplicate := titles[field.Title]; duplicate {
			return fmt.Errorf("线上字段标题重复: %s", field.Title)
		}
		if _, duplicate := ids[field.ID]; duplicate {
			return fmt.Errorf("线上字段 ID 重复: %s", field.ID)
		}
		titles[field.Title] = struct{}{}
		ids[field.ID] = struct{}{}
	}
	return nil
}

func schemaFieldSlices(schema config.Schema) map[string][]config.Field {
	result := make(map[string][]config.Field, len(schema.Roles))
	for roleName, fields := range schema.Roles {
		for _, field := range fields {
			result[roleName] = append(result[roleName], field)
		}
	}
	return result
}

func contractDigest(fieldsByRole map[string][]config.Field) string {
	normalized := make(map[string][]config.Field, len(fieldsByRole))
	for roleName, fields := range fieldsByRole {
		items := append([]config.Field(nil), fields...)
		sort.Slice(items, func(i, j int) bool {
			if items[i].ID == items[j].ID {
				return items[i].Title < items[j].Title
			}
			return items[i].ID < items[j].ID
		})
		normalized[roleName] = items
	}
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func schemaDifferences(local config.Schema, online map[string][]config.Field) ([]map[string]string, map[string]int, map[string]int) {
	differences := []map[string]string{}
	onlineCounts := map[string]int{}
	localCounts := map[string]int{}
	roles := make([]string, 0, len(validRoles))
	for roleName := range validRoles {
		roles = append(roles, roleName)
	}
	sort.Strings(roles)
	for _, roleName := range roles {
		onlineCounts[roleName] = len(online[roleName])
		localCounts[roleName] = len(local.Roles[roleName])
		onlineByTitle := map[string]config.Field{}
		for _, field := range online[roleName] {
			onlineByTitle[field.Title] = field
		}
		for title, onlineField := range onlineByTitle {
			localField, found := local.Roles[roleName][title]
			if !found {
				differences = append(differences, map[string]string{"target_role": roleName, "change": "added_online", "field_title": title, "online_field_id": onlineField.ID, "online_field_type": onlineField.Type})
				continue
			}
			if !reflect.DeepEqual(localField, onlineField) {
				differences = append(differences, map[string]string{"target_role": roleName, "change": "changed_online", "field_title": title, "online_field_id": onlineField.ID, "online_field_type": onlineField.Type, "local_field_id": localField.ID, "local_field_type": localField.Type})
			}
		}
		for title, localField := range local.Roles[roleName] {
			if _, found := onlineByTitle[title]; !found {
				differences = append(differences, map[string]string{"target_role": roleName, "change": "removed_online", "field_title": title, "local_field_id": localField.ID, "local_field_type": localField.Type})
			}
		}
	}
	sort.Slice(differences, func(i, j int) bool {
		left := differences[i]["target_role"] + "\x00" + differences[i]["field_title"] + "\x00" + differences[i]["change"]
		right := differences[j]["target_role"] + "\x00" + differences[j]["field_title"] + "\x00" + differences[j]["change"]
		return left < right
	})
	return differences, onlineCounts, localCounts
}

func mirrorFields(rawFields []map[string]any) ([]config.Field, error) {
	fields := make([]config.Field, 0, len(rawFields))
	for _, raw := range rawFields {
		title, titleOK := raw["field_title"].(string)
		id, idOK := raw["field_id"].(string)
		fieldType, typeOK := raw["field_type"].(string)
		if !titleOK || !idOK || !typeOK || title == "" || id == "" || fieldType == "" {
			return nil, fmt.Errorf("线上字段定义不完整")
		}
		field := config.Field{Title: title, ID: id, Type: fieldType}
		if fieldType == "FIELD_TYPE_REFERENCE" {
			property := referenceProperty(raw)
			field.ReferenceTargetSheetID, _ = property["sub_id"].(string)
			field.ReferenceTargetFieldID, _ = property["field_id"].(string)
			if isMultiple, ok := property["is_multiple"].(bool); ok {
				field.ReferenceIsMultiple = &isMultiple
			}
		}
		if fieldType == "FIELD_TYPE_SINGLE_SELECT" || fieldType == "FIELD_TYPE_MULTI_SELECT" || fieldType == "FIELD_TYPE_SELECT" {
			property, _ := raw["property"].(map[string]any)
			if property == nil {
				if fieldType == "FIELD_TYPE_SINGLE_SELECT" {
					property, _ = raw["property_single_select"].(map[string]any)
				}
				if fieldType == "FIELD_TYPE_SELECT" || fieldType == "FIELD_TYPE_MULTI_SELECT" {
					property, _ = raw["property_select"].(map[string]any)
				}
			}
			options, _ := property["options"].([]any)
			field.Options = map[string]string{}
			for _, item := range options {
				option, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name, nameOK := option["name"].(string)
				if !nameOK {
					name, nameOK = option["text"].(string)
				}
				optionID, idOK := option["id"].(string)
				if nameOK && idOK && name != "" && optionID != "" {
					field.Options[name] = optionID
				}
			}
			// The current official get_fields response provides field identity and
			// type, but may omit select-option IDs. Preserve that absence so record
			// writes fail closed rather than manufacturing an option codec.
			if len(field.Options) == 0 {
				field.Options = nil
			}
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func referenceProperty(field map[string]any) map[string]any {
	if property, ok := field["property_reference"].(map[string]any); ok {
		if value, ok := property["sub_id"].(string); ok && value != "" {
			return property
		}
	}
	return nestedMapWithKey(field, "sub_id")
}

func nestedMapWithKey(value any, key string) map[string]any {
	switch current := value.(type) {
	case map[string]any:
		if direct, ok := current[key].(string); ok && direct != "" {
			return current
		}
		keys := make([]string, 0, len(current))
		for childKey := range current {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		for _, childKey := range keys {
			if found := nestedMapWithKey(current[childKey], key); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range current {
			if found := nestedMapWithKey(item, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func empty(raw json.RawMessage) error {
	var value map[string]any
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &value); err != nil || len(value) != 0 {
		return fmt.Errorf("此工具不接受参数")
	}
	return nil
}
func strictDecode(raw json.RawMessage, target any, allowed ...string) error {
	var check map[string]json.RawMessage
	if err := json.Unmarshal(raw, &check); err != nil {
		return fmt.Errorf("参数必须是对象")
	}
	permitted := map[string]bool{}
	for _, key := range allowed {
		permitted[key] = true
	}
	for key := range check {
		if !permitted[key] {
			return fmt.Errorf("不接受字段 %s", key)
		}
	}
	return json.Unmarshal(raw, target)
}

type recordInput struct {
	RecordID string         `json:"record_id"`
	Values   map[string]any `json:"values"`
}
type applyInput struct {
	TargetRole, Operation, IdempotencyKey, SourceRevision string
	Records                                               []recordInput
}

func (s *Server) apply(ctx context.Context, runtime config.Config, schema config.Schema, client *wecom.Client, raw json.RawMessage) (any, error) {
	var wire struct {
		TargetRole     string        `json:"target_role"`
		Operation      string        `json:"operation"`
		IdempotencyKey string        `json:"idempotency_key"`
		SourceRevision string        `json:"source_revision"`
		Records        []recordInput `json:"records"`
	}
	if err := strictDecode(raw, &wire, "target_role", "operation", "idempotency_key", "source_revision", "records"); err != nil {
		return nil, err
	}
	input := applyInput{wire.TargetRole, wire.Operation, wire.IdempotencyKey, wire.SourceRevision, wire.Records}
	if err := role(input.TargetRole); err != nil {
		return nil, err
	}
	if input.Operation != "add_records" && input.Operation != "update_records" {
		return nil, fmt.Errorf("operation 仅允许 add_records 或 update_records")
	}
	if !runtime.Allows(input.Operation) {
		return nil, fmt.Errorf("实例白名单未允许 %s", input.Operation)
	}
	if input.TargetRole == "Z-S03" && !runtime.Allows("update_records") {
		return nil, fmt.Errorf("实例白名单未允许 update_records，拒绝写入会导致需求进度无法同步的任务")
	}
	if len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 || len(input.SourceRevision) == 0 || len(input.SourceRevision) > 256 {
		return nil, fmt.Errorf("idempotency_key 或 source_revision 无效")
	}
	if len(input.Records) == 0 || len(input.Records) > 50 {
		return nil, fmt.Errorf("records 必须为 1 至 50 项")
	}
	input, err := withRequirementProgressDefaults(schema, input)
	if err != nil {
		return nil, err
	}
	prepared, err := compileRecords(schema, input)
	if err != nil {
		return nil, err
	}
	digest := requestDigest(input, prepared)
	target, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, input.TargetRole, runtime.Allows)
	if err != nil {
		return nil, err
	}
	if input.TargetRole == "Z-S03" {
		s.progressMu.Lock()
		defer s.progressMu.Unlock()
		releaseProgressLock, lockErr := acquireProgressFileLock(ctx, runtime.StatePath)
		if lockErr != nil {
			return nil, lockErr
		}
		defer releaseProgressLock()
	}
	var taskReadbackBefore map[string]any
	if input.TargetRole == "Z-S03" && input.Operation == "update_records" {
		taskReadbackBefore, err = client.Request(ctx, "get_records", map[string]any{"docid": target.DocumentID, "sheet_id": target.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200})
		if err != nil || apiError(taskReadbackBefore) != nil || recordSetMayBeIncomplete(taskReadbackBefore, 200) {
			return nil, fmt.Errorf("读取任务变更前快照失败，拒绝写入以避免需求进度失真")
		}
	}
	// Reserve immediately before the external mutation. Earlier validation,
	// target resolution, and the S03 pre-write snapshot remain safely retryable.
	if err := s.reserve(runtime.StatePath, input.IdempotencyKey, digest); err != nil {
		return nil, err
	}
	result, err := client.Request(ctx, input.Operation, map[string]any{"docid": target.DocumentID, "sheet_id": target.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": prepared})
	if err != nil {
		return nil, err
	}
	if err := apiError(result); err != nil {
		return nil, fmt.Errorf("企业微信写入已发起但未确认成功: %w", err)
	}
	readback, readbackErr := client.Request(ctx, "get_records", map[string]any{"docid": target.DocumentID, "sheet_id": target.SheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200})
	if readbackErr != nil || apiError(readback) != nil {
		return map[string]any{"state": "applied_readback_pending", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false}, nil
	}
	if !verifyReadback(input.Operation, prepared, result, readback) {
		if referenceReadbackGap(input.Operation, prepared, result, readback) {
			if input.TargetRole == "Z-S03" {
				return map[string]any{"state": "applied_progress_sync_pending", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false, "reference_write_accepted": true, "progress_error": "主需求关联回读不完整，未执行需求进度同步", "write_result": writeSummary(result)}, nil
			}
			if err := s.completeState(runtime.StatePath, input.IdempotencyKey, digest); err != nil {
				return map[string]any{"state": "applied_idempotency_completion_pending", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false, "reference_write_accepted": true, "idempotency_error": err.Error(), "write_result": writeSummary(result)}, nil
			}
			return map[string]any{"state": "applied_reference_readback_inconclusive", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false, "reference_write_accepted": true, "write_result": writeSummary(result), "readback_record_count": len(recordsFrom(readback))}, nil
		}
		return map[string]any{"state": "applied_readback_pending", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": false}, nil
	}
	progressSync, progressErr := s.syncRequirementProgress(ctx, runtime, schema, client, target, input, result, taskReadbackBefore, readback)
	if progressErr != nil {
		return map[string]any{
			"state":             "applied_progress_sync_pending",
			"target_role":       input.TargetRole,
			"idempotency_key":   input.IdempotencyKey,
			"source_revision":   input.SourceRevision,
			"request_digest":    digest,
			"readback_verified": true,
			"progress_error":    progressErr.Error(),
			"write_result":      writeSummary(result),
		}, nil
	}
	if err := s.completeState(runtime.StatePath, input.IdempotencyKey, digest); err != nil {
		return map[string]any{"state": "applied_idempotency_completion_pending", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": true, "idempotency_error": err.Error(), "write_result": writeSummary(result)}, nil
	}
	response := map[string]any{"state": "applied", "target_role": input.TargetRole, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "request_digest": digest, "readback_verified": true, "write_result": writeSummary(result), "readback_record_count": len(recordsFrom(readback))}
	if progressSync != nil {
		response["requirement_progress_sync"] = progressSync
	}
	return response, nil
}

func compileRecords(schema config.Schema, input applyInput) ([]map[string]any, error) {
	fields := schema.Roles[input.TargetRole]
	if len(fields) == 0 {
		return nil, fmt.Errorf("Schema 镜像缺少 %s", input.TargetRole)
	}
	result := make([]map[string]any, 0, len(input.Records))
	for _, record := range input.Records {
		if len(record.Values) == 0 {
			return nil, fmt.Errorf("每条记录至少要有一个字段")
		}
		if input.Operation == "update_records" && record.RecordID == "" {
			return nil, fmt.Errorf("update_records 必须提供 record_id")
		}
		if input.Operation == "add_records" && record.RecordID != "" {
			return nil, fmt.Errorf("add_records 不接受 record_id")
		}
		values := map[string]any{}
		for title, value := range record.Values {
			field, exists := fields[title]
			if !exists {
				return nil, fmt.Errorf("%s 未在本地 Schema 镜像 %s 中定义", title, input.TargetRole)
			}
			switch field.Type {
			case "FIELD_TYPE_TEXT":
				text, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("字段 %s 必须是文本", title)
				}
				values[field.ID] = []map[string]string{{"type": "text", "text": text}}
			case "FIELD_TYPE_CHECKBOX":
				checked, ok := value.(bool)
				if !ok {
					return nil, fmt.Errorf("字段 %s 必须是布尔值", title)
				}
				values[field.ID] = checked
			case "FIELD_TYPE_SINGLE_SELECT":
				label, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("字段 %s 必须是已登记的选项名称", title)
				}
				if field.Options[label] == "" {
					return nil, fmt.Errorf("字段 %s 的选项 %s 未在 Owner 授权同步的 Schema 镜像中登记", title, label)
				}
				// The Enterprise WeCom record API accepts Option objects by text,
				// not the option ID returned in field metadata. The mirror check
				// above prevents the API from silently creating an unknown option.
				values[field.ID] = []any{map[string]string{"text": label}}
			case "FIELD_TYPE_NUMBER", "FIELD_TYPE_PROGRESS", "FIELD_TYPE_CURRENCY", "FIELD_TYPE_PERCENTAGE":
				if _, ok := value.(float64); !ok {
					return nil, fmt.Errorf("字段 %s 必须是数字", title)
				}
				values[field.ID] = value
			case "FIELD_TYPE_DATE_TIME", "FIELD_TYPE_PHONE_NUMBER", "FIELD_TYPE_EMAIL", "FIELD_TYPE_BARCODE":
				if _, ok := value.(string); !ok {
					return nil, fmt.Errorf("字段 %s 必须是字符串", title)
				}
				values[field.ID] = value
			case "FIELD_TYPE_IMAGE", "FIELD_TYPE_USER", "FIELD_TYPE_SELECT", "FIELD_TYPE_MULTI_SELECT", "FIELD_TYPE_URL", "FIELD_TYPE_LOCATION", "FIELD_TYPE_WWGROUP":
				if _, ok := value.([]any); !ok {
					return nil, fmt.Errorf("字段 %s 必须使用已验证的单元格数组值", title)
				}
				values[field.ID] = value
			case "FIELD_TYPE_REFERENCE":
				references, err := compileReferenceValue(title, value)
				if err != nil {
					return nil, err
				}
				values[field.ID] = references
			default:
				return nil, fmt.Errorf("字段 %s 的类型 %s 尚未获得有效写入样本，拒绝猜测或写入", title, field.Type)
			}
		}
		item := map[string]any{"values": values}
		if record.RecordID != "" {
			item["record_id"] = record.RecordID
		}
		result = append(result, item)
	}
	return result, nil
}

func compileReferenceValue(title string, value any) ([]any, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("字段 %s 必须是至少一个 {record_id} 对象组成的数组", title)
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || len(entry) != 1 {
			return nil, fmt.Errorf("字段 %s 的关联值必须是仅含 record_id 的对象", title)
		}
		recordID, ok := entry["record_id"].(string)
		if !ok || recordID == "" {
			return nil, fmt.Errorf("字段 %s 的关联值缺少 record_id", title)
		}
		// The API accepts the object form with errcode=0 but, on this tenant,
		// silently drops the association. The string-array wire form is also
		// documented and matches the value shape returned by get_records.
		result = append(result, recordID)
	}
	return result, nil
}

func requestDigest(input applyInput, prepared []map[string]any) string {
	data, _ := json.Marshal(map[string]any{"role": input.TargetRole, "operation": input.Operation, "idempotency_key": input.IdempotencyKey, "source_revision": input.SourceRevision, "records": prepared})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type idempotencyState struct {
	Entries map[string]stateEntry `json:"entries"`
}
type stateEntry struct {
	Digest string `json:"digest"`
	Status string `json:"status"`
}

func (s *Server) reserve(path, key, digest string) error {
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
		if old.Digest != digest {
			return fmt.Errorf("idempotency_key 已绑定其他变更")
		}
		if old.Status == "completed" {
			return fmt.Errorf("此变更已完成，禁止重复执行")
		}
		return fmt.Errorf("此变更此前已保留但未完成回读，请先恢复核验，禁止盲目重试")
	}
	state.Entries[key] = stateEntry{Digest: digest, Status: "pending"}
	return saveState(path, state)
}
func (s *Server) complete(path, key, digest string) {
	_ = s.completeState(path, key, digest)
}

func (s *Server) completeState(path, key, digest string) error {
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
	if state.Entries[key].Digest != digest {
		return fmt.Errorf("幂等状态与已应用变更不一致")
	}
	state.Entries[key] = stateEntry{Digest: digest, Status: "completed"}
	if err := saveState(path, state); err != nil {
		return fmt.Errorf("保存幂等完成状态失败")
	}
	return nil
}
func loadState(path string) (idempotencyState, error) {
	result := idempotencyState{Entries: map[string]stateEntry{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("读取本地幂等状态失败")
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Entries == nil {
		return result, fmt.Errorf("本地幂等状态无效，拒绝写入")
	}
	return result, nil
}
func saveState(path string, state idempotencyState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func recordsFrom(response map[string]any) []any {
	result, _ := response["result"].(map[string]any)
	records, _ := result["records"].([]any)
	return records
}
func apiError(response map[string]any) error {
	result, _ := response["result"].(map[string]any)
	code, exists := result["errcode"]
	if !exists {
		return nil
	}
	number, numeric := code.(float64)
	if numeric && number != 0 {
		message, _ := result["errmsg"].(string)
		return fmt.Errorf("errcode %.0f: %s", number, message)
	}
	return nil
}
func writeSummary(response map[string]any) map[string]any {
	result, _ := response["result"].(map[string]any)
	summary := map[string]any{}
	if code, ok := result["errcode"]; ok {
		summary["errcode"] = code
	}
	if message, ok := result["errmsg"].(string); ok {
		summary["errmsg"] = message
	}
	return summary
}

func verifyReadback(operation string, prepared []map[string]any, writeResult, readback map[string]any) bool {
	records := recordsFrom(readback)
	if operation == "update_records" {
		for _, expected := range prepared {
			id, _ := expected["record_id"].(string)
			if id == "" || !containsExpectedRecord(records, id, expected["values"].(map[string]any)) {
				return false
			}
		}
		return true
	}
	// An add response must return record IDs before we can prove that a matching
	// row is the one just created instead of an older identical row.
	ids := writeRecordIDs(writeResult)
	if len(ids) != len(prepared) {
		return false
	}
	for index, id := range ids {
		if !containsExpectedRecord(records, id, prepared[index]["values"].(map[string]any)) {
			return false
		}
	}
	return true
}

func containsExpectedRecord(records []any, recordID string, expectedValues map[string]any) bool {
	for _, item := range records {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := record["record_id"].(string)
		if id != recordID {
			continue
		}
		values, _ := record["values"].(map[string]any)
		for fieldID, expected := range expectedValues {
			actual, found := values[fieldID]
			if !found || !sameCellValue(actual, expected) {
				return false
			}
		}
		return true
	}
	return false
}

func sameCellValue(actual, expected any) bool {
	if canonicalValue(actual) == canonicalValue(expected) {
		return true
	}
	expectedReferences, expectedReferenceOK := referenceRecordIDs(expected)
	actualReferences, actualReferenceOK := referenceRecordIDs(actual)
	if expectedReferenceOK && actualReferenceOK {
		return canonicalValue(expectedReferences) == canonicalValue(actualReferences)
	}
	expectedOptions, expectedOK := optionTexts(expected)
	actualOptions, actualOK := optionTexts(actual)
	return expectedOK && actualOK && canonicalValue(expectedOptions) == canonicalValue(actualOptions)
}

func referenceRecordIDs(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		var recordID string
		switch entry := item.(type) {
		case string:
			recordID = entry
		case map[string]any:
			recordID, _ = entry["record_id"].(string)
			if recordID == "" || len(entry) != 1 {
				return nil, false
			}
		case map[string]string:
			recordID = entry["record_id"]
			if recordID == "" || len(entry) != 1 {
				return nil, false
			}
		default:
			return nil, false
		}
		if recordID == "" {
			return nil, false
		}
		ids = append(ids, recordID)
	}
	sort.Strings(ids)
	return ids, true
}

func optionTexts(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, false
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		switch entry := item.(type) {
		case map[string]any:
			if entry["type"] != nil {
				return nil, false
			}
			text, ok := entry["text"].(string)
			if !ok || text == "" {
				return nil, false
			}
			texts = append(texts, text)
		case map[string]string:
			text := entry["text"]
			if text == "" || entry["type"] != "" {
				return nil, false
			}
			texts = append(texts, text)
		default:
			return nil, false
		}
	}
	sort.Strings(texts)
	return texts, true
}

// referenceReadbackGap recognizes the currently observed WeCom behavior: the
// write API accepts an official reference value but get_records returns an
// empty array for that cell. It is deliberately narrower than normal
// readback verification: the target record and every non-reference value must
// still be present and exact, and at least one expected reference must become
// an empty array. This is a completed write with incomplete observation, not a
// retryable write failure.
func referenceReadbackGap(operation string, prepared []map[string]any, writeResult, readback map[string]any) bool {
	ids := []string{}
	if operation == "update_records" {
		for _, expected := range prepared {
			id, _ := expected["record_id"].(string)
			ids = append(ids, id)
		}
	} else {
		ids = writeRecordIDs(writeResult)
	}
	if len(ids) != len(prepared) {
		return false
	}
	records := recordsFrom(readback)
	for index, recordID := range ids {
		expectedValues, _ := prepared[index]["values"].(map[string]any)
		matched := false
		for _, item := range records {
			row, ok := item.(map[string]any)
			if !ok || row["record_id"] != recordID {
				continue
			}
			values, _ := row["values"].(map[string]any)
			sawReferenceGap := false
			allOtherValuesMatch := true
			for fieldID, expected := range expectedValues {
				actual, found := values[fieldID]
				if isReferenceValue(expected) {
					if !found || !isEmptyArray(actual) {
						allOtherValuesMatch = false
						break
					}
					sawReferenceGap = true
					continue
				}
				if !found || !sameCellValue(actual, expected) {
					allOtherValuesMatch = false
					break
				}
			}
			if allOtherValuesMatch && sawReferenceGap {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func isReferenceValue(value any) bool {
	_, ok := referenceRecordIDs(value)
	return ok
}

func isEmptyArray(value any) bool {
	items, ok := value.([]any)
	return ok && len(items) == 0
}

func writeRecordIDs(response map[string]any) []string {
	result, _ := response["result"].(map[string]any)
	records, _ := result["records"].([]any)
	ids := make([]string, 0, len(records))
	for _, item := range records {
		if record, ok := item.(map[string]any); ok {
			if id, ok := record["record_id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func canonicalValue(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
func sanitizedReadResult(response map[string]any, targetRole string) map[string]any {
	return map[string]any{"target_role": targetRole, "record_count": len(recordsFrom(response)), "records": recordsFrom(response)}
}
