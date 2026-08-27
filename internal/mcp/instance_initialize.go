package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/zoopschema"
)

var initializeIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

var initializeJournalPhases = map[string]struct{}{
	"registry_resolving": {}, "registry_identity_known": {}, "registry_schema_verified": {},
	"business_resolving": {}, "business_identity_known": {}, "zoop_sheets_reconciled": {}, "registry_row_verified": {},
	"schema_staged": {}, "candidate_smoke_verified": {}, "config_committed": {}, "final_smoke_verified": {}, "ready": {}, "recovery_required": {},
	"conflict": {}, "environment_unavailable": {},
}

const (
	instanceInitializeAuthorization = "initialize_or_reconcile_default_zoop_instance"
	instanceInitializeGroup         = "instance_initialize"
	instanceInitializeJournalV1     = "instance-initialize-v1"
	instanceInitializePreviewTTL    = 10 * time.Minute
)

type initializePreview struct {
	SnapshotDigest string
	ExpiresAt      time.Time
}

type instanceInitializeJournal struct {
	Version            string `json:"version"`
	Phase              string `json:"phase"`
	AssetKind          string `json:"asset_kind,omitempty"`
	OperationID        string `json:"operation_id,omitempty"`
	RegistryDocumentID string `json:"registry_document_id,omitempty"`
	BusinessDocumentID string `json:"business_document_id,omitempty"`
	RegistryOwned      bool   `json:"registry_owned_by_create,omitempty"`
	BusinessOwned      bool   `json:"business_owned_by_create,omitempty"`
	PreviewID          string `json:"preview_id,omitempty"`
	CatalogDigest      string `json:"catalog_digest,omitempty"`
	ConfigDigest       string `json:"config_digest,omitempty"`
	LastErrorCode      string `json:"last_error_code,omitempty"`
	UpdatedAt          string `json:"updated_at"`
}

type initializeStatusInput struct {
	RegistryDocumentID         string `json:"registry_document_id"`
	RecoveryRegistryDocumentID string `json:"recovery_registry_document_id"`
	RecoveryBusinessDocumentID string `json:"recovery_business_document_id"`
}

type initializeSnapshot struct {
	InstanceName             string            `json:"instance_name"`
	ConfigDigest             string            `json:"config_digest"`
	JournalDigest            string            `json:"journal_digest"`
	CatalogDigest            string            `json:"catalog_digest"`
	CatalogVersion           string            `json:"catalog_version"`
	CatalogCreationComplete  bool              `json:"catalog_creation_complete"`
	InputRegistryDocumentID  string            `json:"input_registry_document_id"`
	RecoveryRegistryInput    string            `json:"recovery_registry_input"`
	RecoveryBusinessInput    string            `json:"recovery_business_input"`
	RegistryDocumentID       string            `json:"registry_document_id"`
	RegistryOwnedByJournal   bool              `json:"registry_owned_by_journal"`
	RegistryIdentityDigest   string            `json:"registry_identity_digest"`
	RegistryAuthDigest       string            `json:"registry_auth_digest"`
	RegistrySheetID          string            `json:"registry_sheet_id"`
	RegistryFieldsDigest     string            `json:"registry_fields_digest"`
	RegistryRecordsComplete  bool              `json:"registry_records_complete"`
	ActiveRegistryCount      int               `json:"active_registry_count"`
	ActiveRegistryRecordID   string            `json:"active_registry_record_id"`
	ActiveRegistryRowsDigest string            `json:"active_registry_rows_digest"`
	BusinessDocumentID       string            `json:"business_document_id"`
	BusinessOwnedByJournal   bool              `json:"business_owned_by_journal"`
	BusinessIdentityDigest   string            `json:"business_identity_digest"`
	BusinessAuthDigest       string            `json:"business_auth_digest"`
	RoleSheetIDs             map[string]string `json:"role_sheet_ids"`
	RoleFieldsDigests        map[string]string `json:"role_fields_digests"`
	LocalSchemaDigest        string            `json:"local_schema_digest"`
	SmokeVerified            bool              `json:"smoke_verified"`
}

type initializeObservation struct {
	Snapshot          initializeSnapshot
	State             string
	Invariants        []string
	Conflicts         []string
	PlannedOperations []string
	FieldCounts       map[string]int
	JournalPhase      string
	SmokeRecordCount  int
	SnapshotComplete  bool
	RecoveryAssetKind string
	RecoveryOperation string
}

func (s *Server) currentInitializeCatalog() (zoopschema.Catalog, error) {
	if s != nil && s.initializeCatalog != nil {
		return s.initializeCatalog()
	}
	return zoopschema.Current()
}

func executableInitializeRecovery(observation initializeObservation) bool {
	if observation.State != "recovery_required" || !observation.SnapshotComplete || len(observation.Conflicts) != 0 {
		return false
	}
	for _, operation := range observation.PlannedOperations {
		if strings.HasPrefix(operation, "bind_recovery_") {
			return false
		}
	}
	switch observation.RecoveryAssetKind {
	case "registry":
		return observation.Snapshot.RegistryDocumentID != "" && observation.Snapshot.RegistryIdentityDigest != "" && observation.Snapshot.RegistrySheetID != ""
	case "business":
		return observation.Snapshot.BusinessDocumentID != "" && observation.Snapshot.BusinessIdentityDigest != ""
	default:
		return false
	}
}

func instanceInitializeStatusToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"registry_document_id":          map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
			"recovery_registry_document_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
			"recovery_business_document_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
		},
	}
}

func instanceInitializeApplyToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"preview_id", "preview_expires_at", "owner_authorization"},
		"properties": map[string]any{
			"preview_id":                    map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"},
			"preview_expires_at":            map[string]any{"type": "string", "format": "date-time"},
			"owner_authorization":           map[string]any{"const": instanceInitializeAuthorization},
			"registry_document_id":          map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
			"recovery_registry_document_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
			"recovery_business_document_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,256}$"},
		},
	}
}

func (s *Server) instanceInitializeStatus(ctx context.Context, runtime config.Config, client wecomRequester, clientErr error, raw json.RawMessage) (any, error) {
	var input initializeStatusInput
	if err := strictDecode(raw, &input, "registry_document_id", "recovery_registry_document_id", "recovery_business_document_id"); err != nil {
		return nil, err
	}
	catalog, catalogErr := s.currentInitializeCatalog()
	observation := observeInstanceInitializationWithCatalog(ctx, runtime, client, clientErr, input, catalog, catalogErr)
	snapshotDigest := initializeObservationDigest(observation)
	expiresAt := time.Now().UTC().Add(instanceInitializePreviewTTL)
	expiresAtText := ""
	previewID := ""
	if observation.SnapshotComplete && len(observation.Conflicts) == 0 && (observation.State == "ready" || observation.State == "changes_planned" || executableInitializeRecovery(observation)) {
		expiresAtText = expiresAt.Format(time.RFC3339Nano)
		previewID = digestValue(map[string]any{"snapshot_digest": snapshotDigest, "expires_at": expiresAtText})
		s.previewMu.Lock()
		if s.previews == nil {
			s.previews = map[string]initializePreview{}
		}
		for id, preview := range s.previews {
			if time.Now().After(preview.ExpiresAt) {
				delete(s.previews, id)
			}
		}
		s.previews[previewID] = initializePreview{SnapshotDigest: snapshotDigest, ExpiresAt: expiresAt}
		s.previewMu.Unlock()
	}
	nextRequiredInput := ""
	if observation.State == "recovery_required" && observation.RecoveryAssetKind != "" {
		if observation.RecoveryAssetKind != "business" || observation.Snapshot.BusinessDocumentID == "" {
			nextRequiredInput = "recovery_" + observation.RecoveryAssetKind + "_document_id"
		}
	}

	return map[string]any{
		"state":                      observation.State,
		"capability_gap":             observation.State == "capability_gap",
		"instance_name":              runtime.InstanceName,
		"expected_schema_version":    observation.Snapshot.CatalogVersion,
		"catalog_creation_complete":  observation.Snapshot.CatalogCreationComplete,
		"journal_phase":              observation.JournalPhase,
		"observed":                   publicInitializeObservation(observation),
		"invariants":                 observation.Invariants,
		"conflicts":                  observation.Conflicts,
		"planned_operations":         observation.PlannedOperations,
		"preview_id":                 previewID,
		"preview_snapshot_digest":    snapshotDigest,
		"expires_at":                 expiresAtText,
		"snapshot_complete":          observation.SnapshotComplete,
		"recovery_asset_kind":        observation.RecoveryAssetKind,
		"recovery_operation_id":      observation.RecoveryOperation,
		"next_required_input":        nextRequiredInput,
		"online_read_only":           true,
		"local_updated":              false,
		"enterprise_wecom_updated":   false,
		"business_records_disclosed": false,
	}, nil
}

func (s *Server) instanceInitializeApply(ctx context.Context, runtime config.Config, client wecomRequester, clientErr error, raw json.RawMessage) (any, error) {
	var input struct {
		PreviewID                  string `json:"preview_id"`
		PreviewExpiresAt           string `json:"preview_expires_at"`
		OwnerAuthorization         string `json:"owner_authorization"`
		RegistryDocumentID         string `json:"registry_document_id"`
		RecoveryRegistryDocumentID string `json:"recovery_registry_document_id"`
		RecoveryBusinessDocumentID string `json:"recovery_business_document_id"`
	}
	if err := strictDecode(raw, &input, "preview_id", "preview_expires_at", "owner_authorization", "registry_document_id", "recovery_registry_document_id", "recovery_business_document_id"); err != nil {
		return nil, err
	}
	if input.OwnerAuthorization != instanceInitializeAuthorization {
		return nil, fmt.Errorf("缺少实例初始化固定 Owner 授权")
	}
	s.previewMu.Lock()
	preview, exists := s.previews[input.PreviewID]
	s.previewMu.Unlock()
	if !exists || time.Now().After(preview.ExpiresAt) {
		return nil, fmt.Errorf("实例初始化预览不存在或已过期，请重新执行只读 status")
	}
	parsedExpiry, err := time.Parse(time.RFC3339Nano, input.PreviewExpiresAt)
	if err != nil || !parsedExpiry.Equal(preview.ExpiresAt) {
		return nil, fmt.Errorf("实例初始化 preview_expires_at 与 status 返回值不一致")
	}
	catalog, catalogErr := s.currentInitializeCatalog()
	observation := observeInstanceInitializationWithCatalog(ctx, runtime, client, clientErr, initializeStatusInput{
		RegistryDocumentID:         input.RegistryDocumentID,
		RecoveryRegistryDocumentID: input.RecoveryRegistryDocumentID,
		RecoveryBusinessDocumentID: input.RecoveryBusinessDocumentID,
	}, catalog, catalogErr)
	if initializeObservationDigest(observation) != preview.SnapshotDigest {
		return nil, fmt.Errorf("实例初始化预览已失效，请重新执行只读 status")
	}
	if observation.State != "ready" && observation.State != "changes_planned" && !executableInitializeRecovery(observation) {
		return nil, fmt.Errorf("实例初始化当前状态 %s 不可执行", observation.State)
	}
	if len(observation.Conflicts) != 0 || !observation.SnapshotComplete {
		return nil, fmt.Errorf("实例初始化观察不完整或存在冲突，拒绝写入")
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(runtime.StatePath + ".instance-initialize")
	if err != nil {
		return nil, fmt.Errorf("获取实例初始化锁失败")
	}
	defer release()

	if catalogErr != nil {
		return nil, fmt.Errorf("实例初始化 catalog 无效")
	}
	remotePlanned := hasRemoteInitializePlan(observation.PlannedOperations)
	if requiresCompleteInitializeCatalog(observation.PlannedOperations) && !catalog.CompleteForCreation {
		return nil, fmt.Errorf("Zoop catalog 尚不具备完整创建契约（缺少已验证的字段创建属性），未执行任何线上或本地写入")
	}
	journal, _, journalExists, journalErr := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	if journalErr != nil {
		return nil, fmt.Errorf("实例初始化 journal 无效")
	}
	if !journalExists {
		journal = instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "schema_staged"}
	}
	journal.PreviewID = input.PreviewID
	journal.CatalogDigest = observation.Snapshot.CatalogDigest
	journal.ConfigDigest = observation.Snapshot.ConfigDigest
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if journal.RegistryDocumentID == "" {
		journal.RegistryDocumentID = observation.Snapshot.RegistryDocumentID
	}
	if journal.BusinessDocumentID == "" {
		journal.BusinessDocumentID = observation.Snapshot.BusinessDocumentID
	}
	if remotePlanned {
		return s.applyRemoteInstanceInitialization(ctx, runtime, client, observation, catalog, journal)
	}
	if observation.State == "ready" {
		return initializeApplyResult("ready", observation, false, false, ""), nil
	}
	return s.commitObservedInstanceInitialization(ctx, runtime, client, observation, catalog, journal, false)
}

func initializeApplyResult(state string, observation initializeObservation, remoteUpdated, localUpdated bool, backupPath string) map[string]any {
	result := map[string]any{
		"state": state, "instance_name": observation.Snapshot.InstanceName,
		"registry_verified":        observation.Snapshot.RegistryDocumentID != "" && observation.Snapshot.RegistrySheetID != "",
		"nine_tables_verified":     len(observation.Snapshot.RoleSheetIDs) == 9,
		"schema_synced":            observation.Snapshot.LocalSchemaDigest != "" || localUpdated,
		"tools_call_verified":      observation.Snapshot.SmokeVerified,
		"enterprise_wecom_updated": remoteUpdated, "local_updated": localUpdated,
		"owner_accepted": false, "production_deployed": false,
	}
	if backupPath != "" {
		result["config_backup_created"] = true
	}
	return result
}

func (s *Server) commitObservedInstanceInitialization(ctx context.Context, runtime config.Config, client wecomRequester, observation initializeObservation, catalog zoopschema.Catalog, journal instanceInitializeJournal, remoteUpdated bool) (any, error) {
	if s.store == nil {
		return nil, fmt.Errorf("实例配置 store 不可用")
	}
	fieldsByRole, err := readInitializeFieldsByRole(ctx, client, observation.Snapshot.BusinessDocumentID, observation.Snapshot.RoleSheetIDs)
	if err != nil {
		return nil, fmt.Errorf("生成 Schema 前线上字段回读失败")
	}
	generationPath, generationDigest, err := config.WriteOnlineMirrorGeneration(runtime.StatePath, fieldsByRole, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("Schema generation 写入或回读失败: %w", err)
	}
	journal.Phase, journal.ConfigDigest, journal.UpdatedAt = "schema_staged", generationDigest, time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, fmt.Errorf("Schema staged journal 写入失败")
	}
	if err := initializeSmoke(ctx, client, observation.Snapshot.BusinessDocumentID, observation.Snapshot.RoleSheetIDs["Z-S01"]); err != nil {
		return nil, err
	}
	journal.Phase, journal.UpdatedAt = "candidate_smoke_verified", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, fmt.Errorf("candidate smoke journal 写入失败")
	}
	generation := "init-" + generationDigest[:16]
	backupPath, err := s.store.CommitInitialized(config.InitializationCommit{
		RegistryDocumentID: observation.Snapshot.RegistryDocumentID, RegistrySheetID: observation.Snapshot.RegistrySheetID,
		SchemaMirrorPath: generationPath, SchemaVersion: catalog.Version, SchemaDigest: generationDigest, InitializationGeneration: generation,
	})
	if err != nil {
		return nil, fmt.Errorf("实例配置原子提交失败: %w", err)
	}
	journal.Phase, journal.UpdatedAt = "config_committed", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, fmt.Errorf("配置已提交但 journal 待恢复")
	}
	persisted, err := s.store.Current()
	if err != nil || persisted.RegistryDocumentID != observation.Snapshot.RegistryDocumentID || persisted.RegistrySheetID != observation.Snapshot.RegistrySheetID || persisted.SchemaMirrorPath != generationPath || persisted.SchemaDigest != generationDigest {
		return nil, fmt.Errorf("实例配置提交后回读失败")
	}
	if _, err := config.LoadSchema(persisted.SchemaMirrorPath); err != nil {
		return nil, fmt.Errorf("实例配置提交后 Schema 回读失败")
	}
	target, err := wecom.ResolveTarget(ctx, client, persisted.RegistryDocumentID, persisted.RegistryKey, "Z-S01", persisted.Allows)
	if err != nil {
		return nil, fmt.Errorf("配置提交后正常 Resolver 未能解析 Z-S01: %w", err)
	}
	if err := initializeSmoke(ctx, client, target.DocumentID, target.SheetID); err != nil {
		return nil, fmt.Errorf("配置提交后正常 Resolver smoke 失败: %w", err)
	}
	journal.Phase, journal.UpdatedAt = "ready", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, fmt.Errorf("最终 smoke 已通过但 ready journal 待恢复")
	}
	observation.Snapshot.LocalSchemaDigest = generationDigest
	observation.Snapshot.SmokeVerified = true
	return initializeApplyResult("ready", observation, remoteUpdated, true, backupPath), nil
}

func readInitializeFieldsByRole(ctx context.Context, client wecomRequester, documentID string, roleSheetIDs map[string]string) (map[string][]config.Field, error) {
	result := map[string][]config.Field{}
	for index := 1; index <= 9; index++ {
		role := fmt.Sprintf("Z-S%02d", index)
		sheetID := roleSheetIDs[role]
		if sheetID == "" {
			return nil, fmt.Errorf("缺少 %s", role)
		}
		response, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
		if err != nil || apiError(response) != nil {
			return nil, fmt.Errorf("读取 %s 字段失败", role)
		}
		fields, err := mirrorFieldsFromAny(resultSlice(response, "fields"))
		if err != nil {
			return nil, err
		}
		for _, field := range fields {
			result[role] = append(result[role], field)
		}
	}
	return result, nil
}

func initializeSmoke(ctx context.Context, client wecomRequester, documentID, sheetID string) error {
	response, err := client.Request(ctx, "get_records", map[string]any{"docid": documentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 1})
	if err != nil || apiError(response) != nil {
		return fmt.Errorf("Z-S01 只读 smoke 未通过")
	}
	return nil
}

func (s *Server) applyRemoteInstanceInitialization(ctx context.Context, runtime config.Config, client wecomRequester, observation initializeObservation, catalog zoopschema.Catalog, journal instanceInitializeJournal) (any, error) {
	if client == nil {
		return nil, fmt.Errorf("企业微信客户端不可用")
	}
	for _, operation := range []string{"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records", "create_smartsheet", "add_sheet", "update_sheet", "add_fields", "update_fields", "add_records", "delete_records"} {
		if !runtime.AllowsInGroup(instanceInitializeGroup, operation) {
			return nil, fmt.Errorf("实例初始化专用 capability 未允许 %s；initializer 不会自行提升白名单", operation)
		}
	}
	persistRecovery := func(assetKind, errorCode string, cause error) error {
		journal.Phase, journal.AssetKind, journal.LastErrorCode = "recovery_required", assetKind, errorCode
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
			return fmt.Errorf("%v；recovery journal 写入失败: %w", cause, err)
		}
		return cause
	}
	registryID := observation.Snapshot.RegistryDocumentID
	registryCreated := false
	if registryID == "" {
		journal.Phase, journal.AssetKind, journal.OperationID = "registry_resolving", "registry", digestValue(map[string]string{"instance": runtime.InstanceName, "asset": "registry", "catalog": journal.CatalogDigest})
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
			return nil, err
		}
		created, err := client.Request(ctx, "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "SMART_SHEETS_IDS"})
		registryID = initializeCreatedDocumentID(created)
		if registryID != "" {
			journal.RegistryDocumentID = registryID
			journal.RegistryOwned = true
			journal.Phase, journal.UpdatedAt = "registry_identity_known", time.Now().UTC().Format(time.RFC3339Nano)
			if saveErr := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); saveErr != nil {
				return nil, fmt.Errorf("Registry 已创建但 docid journal 写入失败；禁止重试创建")
			}
		}
		if err != nil || apiError(created) != nil || registryID == "" {
			journal.Phase, journal.AssetKind, journal.LastErrorCode, journal.UpdatedAt = "recovery_required", "registry", "create_result_uncertain", time.Now().UTC().Format(time.RFC3339Nano)
			_ = saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal)
			return nil, fmt.Errorf("Registry 创建结果不确定；已保留 durable journal，禁止自动重试")
		}
		registryCreated = true
	}
	registryOwnedByOperation := registryCreated || observation.Snapshot.RegistryOwnedByJournal && observation.Snapshot.RegistryDocumentID == registryID
	registrySheetID, err := reconcileInitializeRegistry(ctx, client, registryID, registryOwnedByOperation)
	if err != nil {
		return nil, persistRecovery("registry", "registry_reconcile_failed", err)
	}
	journal.RegistryDocumentID, journal.Phase, journal.UpdatedAt = registryID, "registry_schema_verified", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, err
	}
	registryFields, err := registryFieldDefinitions(ctx, client, registryID, registrySheetID)
	if err != nil {
		return nil, err
	}
	records, complete, err := readAllInitializeRecords(ctx, client, registryID, registrySheetID)
	if err != nil || !complete {
		return nil, fmt.Errorf("Registry active row 完整分页失败")
	}
	businessID, activeCount := initializeActiveBusiness(records, registryFields, runtime.RegistryKey)
	if activeCount > 1 || activeCount == 1 && businessID == "" {
		return nil, fmt.Errorf("Registry active row 不唯一")
	}
	if activeCount == 0 && observation.Snapshot.BusinessDocumentID != "" {
		// The status preview already verified this exact recovery asset. Reuse it
		// and register the missing row; never create a replacement document.
		businessID = observation.Snapshot.BusinessDocumentID
	}
	businessCreated := false
	if businessID == "" {
		journal.Phase, journal.AssetKind, journal.OperationID = "business_resolving", "business", digestValue(map[string]string{"instance": runtime.InstanceName, "asset": "business", "catalog": journal.CatalogDigest})
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
			return nil, err
		}
		created, err := client.Request(ctx, "create_smartsheet", map[string]any{"doc_type": 10, "doc_name": "Zoop｜" + runtime.RegistryKey})
		businessID = initializeCreatedDocumentID(created)
		if businessID != "" {
			journal.BusinessDocumentID = businessID
			journal.BusinessOwned = true
			journal.Phase, journal.UpdatedAt = "business_identity_known", time.Now().UTC().Format(time.RFC3339Nano)
			if saveErr := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); saveErr != nil {
				return nil, fmt.Errorf("业务文档已创建但 docid journal 写入失败；禁止重试创建")
			}
		}
		if err != nil || apiError(created) != nil || businessID == "" {
			journal.Phase, journal.AssetKind, journal.LastErrorCode, journal.UpdatedAt = "recovery_required", "business", "create_result_uncertain", time.Now().UTC().Format(time.RFC3339Nano)
			_ = saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal)
			return nil, fmt.Errorf("业务文档创建结果不确定；已保留 durable journal，禁止自动重试")
		}
		businessCreated = true
	}
	businessOwnedByOperation := businessCreated || observation.Snapshot.BusinessOwnedByJournal && observation.Snapshot.BusinessDocumentID == businessID
	roleSheetIDs, err := reconcileInitializeBusiness(ctx, client, businessID, businessCreated, businessOwnedByOperation, catalog)
	if err != nil {
		journal.BusinessDocumentID = businessID
		return nil, persistRecovery("business", "business_reconcile_failed", err)
	}
	journal.BusinessDocumentID, journal.Phase, journal.UpdatedAt = businessID, "zoop_sheets_reconciled", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, err
	}
	if activeCount == 0 {
		if err := addInitializeActiveRow(ctx, client, registryID, registrySheetID, registryFields, runtime, businessID, catalog.Version); err != nil {
			return nil, persistRecovery("business", "registry_row_uncertain", err)
		}
	}
	journal.Phase, journal.UpdatedAt = "registry_row_verified", time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveInstanceInitializeJournal(instanceInitializeJournalPath(runtime), journal); err != nil {
		return nil, err
	}
	verifiedRuntime := runtime
	verifiedRuntime.RegistryDocumentID = registryID
	verifiedRuntime.RegistrySheetID = registrySheetID
	refreshed := observeInstanceInitializationWithCatalog(ctx, verifiedRuntime, client, nil, initializeStatusInput{}, catalog, nil)
	if !refreshed.SnapshotComplete || len(refreshed.Conflicts) != 0 || hasRemoteInitializePlan(refreshed.PlannedOperations) {
		return nil, fmt.Errorf("远端初始化写后完整回读未收敛")
	}
	if refreshed.Snapshot.BusinessDocumentID != businessID || len(refreshed.Snapshot.RoleSheetIDs) != 9 {
		return nil, fmt.Errorf("远端初始化写后业务文档或九表回读不一致")
	}
	for role, sheetID := range roleSheetIDs {
		if refreshed.Snapshot.RoleSheetIDs[role] != sheetID {
			return nil, fmt.Errorf("远端初始化写后 %s 子表 ID 不一致", role)
		}
	}
	return s.commitObservedInstanceInitialization(ctx, runtime, client, refreshed, catalog, journal, true)
}

func initializeCreatedDocumentID(response map[string]any) string {
	result, _ := response["result"].(map[string]any)
	documentID, _ := result["docid"].(string)
	if !initializeIdentifier.MatchString(documentID) {
		return ""
	}
	return documentID
}

func reconcileInitializeRegistry(ctx context.Context, client wecomRequester, documentID string, ownedByOperation bool) (string, error) {
	response, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil || apiError(response) != nil {
		return "", fmt.Errorf("Registry 子表回读失败")
	}
	sheets := smartSheetIdentities(response)
	if len(sheets) != 1 {
		return "", fmt.Errorf("Registry 智能子表不唯一")
	}
	if ownedByOperation {
		expected := map[string]string{}
		for _, title := range registryBootstrapFields {
			expected[title] = "FIELD_TYPE_TEXT"
		}
		if err := normalizeOwnedInitializeSheetContract(ctx, client, documentID, sheets[0].ID, "registry_key", expected); err != nil {
			return "", err
		}
	}
	fields, err := registryFieldDefinitions(ctx, client, documentID, sheets[0].ID)
	if err != nil {
		return "", err
	}
	missing := []any{}
	for _, title := range registryBootstrapFields {
		if field, exists := fields[title]; exists {
			if field.Type != "FIELD_TYPE_TEXT" {
				return "", fmt.Errorf("Registry 字段 %s 类型冲突", title)
			}
			continue
		}
		missing = append(missing, map[string]any{"field_title": title, "field_type": "FIELD_TYPE_TEXT"})
	}
	if len(missing) > 0 {
		result, err := client.Request(ctx, "add_fields", map[string]any{"docid": documentID, "sheet_id": sheets[0].ID, "fields": missing})
		if err != nil || apiError(result) != nil {
			return "", fmt.Errorf("Registry 缺失字段补齐失败")
		}
	}
	verified, err := registryFieldDefinitions(ctx, client, documentID, sheets[0].ID)
	if err != nil || len(verified) < len(registryBootstrapFields) {
		return "", fmt.Errorf("Registry 字段回读未通过")
	}
	return sheets[0].ID, nil
}

func initializeActiveBusiness(records []any, fields map[string]config.Field, registryKey string) (string, int) {
	keyID, docID, lifecycleID := fields["registry_key"].ID, fields["docid"].ID, fields["lifecycle_status"].ID
	businessID, count := "", 0
	for _, raw := range records {
		record, _ := raw.(map[string]any)
		values, _ := record["values"].(map[string]any)
		if initializeTextCell(values[keyID]) == registryKey && initializeTextCell(values[lifecycleID]) == "active" {
			count++
			businessID = initializeTextCell(values[docID])
		}
	}
	return businessID, count
}

func reconcileInitializeBusiness(ctx context.Context, client wecomRequester, documentID string, documentCreated, ownedByOperation bool, catalog zoopschema.Catalog) (map[string]string, error) {
	response, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil || apiError(response) != nil {
		return nil, fmt.Errorf("业务文档子表回读失败")
	}
	sheets := smartSheetIdentities(response)
	roleSheetIDs := map[string]string{}
	ownedSheets := map[string]bool{}
	if documentCreated {
		if len(sheets) != 1 {
			return nil, fmt.Errorf("本次新建业务文档的默认子表不唯一")
		}
		if err := renameInitializeSheetAndVerify(ctx, client, documentID, sheets[0].ID, catalog.Roles[0].SheetTitle); err != nil {
			return nil, err
		}
		ownedSheets[sheets[0].ID] = true
	}
	if ownedByOperation && !documentCreated {
		matchedIDs := map[string]bool{}
		for _, role := range catalog.Roles {
			for _, sheet := range sheets {
				if strings.HasPrefix(sheet.Name, role.Role+"｜") {
					matchedIDs[sheet.ID] = true
					ownedSheets[sheet.ID] = true
				}
			}
		}
		zS01Present := false
		for _, sheet := range sheets {
			if strings.HasPrefix(sheet.Name, catalog.Roles[0].Role+"｜") {
				zS01Present = true
			}
		}
		if !zS01Present {
			unclaimed := []sheetIdentity{}
			for _, sheet := range sheets {
				if !matchedIDs[sheet.ID] {
					unclaimed = append(unclaimed, sheet)
				}
			}
			if len(unclaimed) != 1 {
				return nil, fmt.Errorf("恢复业务文档的默认 Z-S01 候选不唯一")
			}
			expected := initializeRoleFieldTypes(catalog.Roles[0])
			if err := normalizeOwnedInitializeSheetContract(ctx, client, documentID, unclaimed[0].ID, catalog.Roles[0].PrimaryFieldTitle, expected); err != nil {
				return nil, fmt.Errorf("恢复 Z-S01 默认模板异常: %w", err)
			}
			if err := renameInitializeSheetAndVerify(ctx, client, documentID, unclaimed[0].ID, catalog.Roles[0].SheetTitle); err != nil {
				return nil, err
			}
			ownedSheets[unclaimed[0].ID] = true
		}
		for _, sheet := range sheets {
			if !matchedIDs[sheet.ID] && !(ownedSheets[sheet.ID] && !zS01Present) {
				return nil, fmt.Errorf("恢复业务文档存在来源不明子表")
			}
		}
	}
	for _, role := range catalog.Roles {
		response, err = client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
		if err != nil || apiError(response) != nil {
			return nil, fmt.Errorf("业务子表回读失败")
		}
		matches := []sheetIdentity{}
		for _, sheet := range smartSheetIdentities(response) {
			if strings.HasPrefix(sheet.Name, role.Role+"｜") {
				matches = append(matches, sheet)
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("%s 子表不唯一", role.Role)
		}
		if len(matches) == 0 {
			result, err := client.Request(ctx, "add_sheet", map[string]any{"docid": documentID, "properties": map[string]any{"title": role.SheetTitle}})
			if err != nil || apiError(result) != nil {
				return nil, fmt.Errorf("新增 %s 子表失败", role.Role)
			}
			response, err = client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
			if err != nil || apiError(response) != nil {
				return nil, fmt.Errorf("新增 %s 后回读失败", role.Role)
			}
			for _, sheet := range smartSheetIdentities(response) {
				if sheet.Name == role.SheetTitle {
					matches = append(matches, sheet)
				}
			}
			if len(matches) != 1 {
				return nil, fmt.Errorf("新增 %s 后未找到唯一子表", role.Role)
			}
			ownedSheets[matches[0].ID] = true
		}
		roleSheetIDs[role.Role] = matches[0].ID
	}
	for _, role := range catalog.Roles {
		if ownedSheets[roleSheetIDs[role.Role]] {
			if err := normalizeOwnedInitializeSheetContract(ctx, client, documentID, roleSheetIDs[role.Role], role.PrimaryFieldTitle, initializeRoleFieldTypes(role)); err != nil {
				return nil, fmt.Errorf("%s 默认模板异常: %w", role.Role, err)
			}
		}
	}
	// Phase one creates tenant-independent fields. References are deferred until
	// every target sheet and primary field ID has been read back.
	for _, role := range catalog.Roles {
		if err := addMissingInitializeFields(ctx, client, documentID, roleSheetIDs[role.Role], role, roleSheetIDs, false); err != nil {
			return nil, err
		}
	}
	for _, role := range catalog.Roles {
		if err := addMissingInitializeFields(ctx, client, documentID, roleSheetIDs[role.Role], role, roleSheetIDs, true); err != nil {
			return nil, err
		}
	}
	return roleSheetIDs, nil
}

func renameInitializeSheetAndVerify(ctx context.Context, client wecomRequester, documentID, sheetID, title string) error {
	result, err := client.Request(ctx, "update_sheet", map[string]any{"docid": documentID, "sheet_id": sheetID, "properties": map[string]any{"title": title}})
	if err != nil || apiError(result) != nil {
		return fmt.Errorf("默认子表改名失败")
	}
	readback, err := client.Request(ctx, "get_sheet", map[string]any{"docid": documentID})
	if err != nil || apiError(readback) != nil {
		return fmt.Errorf("默认子表改名后回读失败")
	}
	for _, sheet := range smartSheetIdentities(readback) {
		if sheet.ID == sheetID && sheet.Name == title {
			return nil
		}
	}
	return fmt.Errorf("默认子表改名后回读未收敛")
}

func initializeRoleFieldTypes(role zoopschema.Role) map[string]string {
	result := map[string]string{}
	for _, field := range role.Fields {
		result[field.Title] = field.Type
	}
	return result
}

// normalizeOwnedInitializeSheet is intentionally callable only for a sheet
// created by the current durable operation. Both create_doc and add_sheet must
// expose exactly one default text primary field. Any other field shape is an
// upstream-template conflict, not cleanup permission.
func normalizeOwnedInitializeSheet(ctx context.Context, client wecomRequester, documentID, sheetID, primaryTitle string) error {
	return normalizeOwnedInitializeSheetContract(ctx, client, documentID, sheetID, primaryTitle, map[string]string{primaryTitle: "FIELD_TYPE_TEXT"})
}

func normalizeOwnedInitializeSheetContract(ctx context.Context, client wecomRequester, documentID, sheetID, primaryTitle string, expected map[string]string) error {
	fieldsResponse, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
	if err != nil || apiError(fieldsResponse) != nil {
		return fmt.Errorf("默认字段读取失败")
	}
	fields := resultSlice(fieldsResponse, "fields")
	// Validate the complete field template before deleting even an empty row.
	if len(fields) == 0 {
		return fmt.Errorf("平台默认主字段数量不是 1")
	}
	primaryFieldID := ""
	defaultFieldID := ""
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		fieldID, _ := field["field_id"].(string)
		title, _ := field["field_title"].(string)
		fieldType, _ := field["field_type"].(string)
		if !initializeIdentifier.MatchString(fieldID) || title == "" || fieldType == "" {
			return fmt.Errorf("平台字段模板包含不可核验字段")
		}
		if expectedType, exists := expected[title]; exists {
			if expectedType != fieldType {
				return fmt.Errorf("平台字段 %s 类型冲突", title)
			}
			if title == primaryTitle {
				primaryFieldID = fieldID
			}
			continue
		}
		if len(fields) == 1 && fieldType == "FIELD_TYPE_TEXT" {
			defaultFieldID = fieldID
			continue
		}
		return fmt.Errorf("平台字段模板包含来源不明字段 %s", title)
	}
	if primaryFieldID == "" && defaultFieldID == "" {
		return fmt.Errorf("平台默认主字段不是唯一文本字段")
	}
	records, complete, err := readAllInitializeRecords(ctx, client, documentID, sheetID)
	if err != nil || !complete {
		return fmt.Errorf("默认记录完整读取失败")
	}
	emptyIDs := []string{}
	for _, raw := range records {
		record, _ := raw.(map[string]any)
		values, _ := record["values"].(map[string]any)
		if !initializeRecordValuesEmpty(values) {
			return fmt.Errorf("本次新建子表出现非空记录，拒绝清理")
		}
		id, _ := record["record_id"].(string)
		if !initializeIdentifier.MatchString(id) {
			return fmt.Errorf("默认空记录缺少可核验 ID")
		}
		emptyIDs = append(emptyIDs, id)
	}
	if len(emptyIDs) > 0 {
		response, err := client.Request(ctx, "delete_records", map[string]any{"docid": documentID, "sheet_id": sheetID, "record_ids": emptyIDs})
		if err != nil || apiError(response) != nil {
			return fmt.Errorf("默认空记录清理失败")
		}
		remaining, complete, err := readAllInitializeRecords(ctx, client, documentID, sheetID)
		if err != nil || !complete || len(remaining) != 0 {
			return fmt.Errorf("默认空记录清理后回读未收敛")
		}
	}
	if primaryFieldID != "" {
		return nil
	}
	response, err := client.Request(ctx, "update_fields", map[string]any{"docid": documentID, "sheet_id": sheetID, "fields": []any{map[string]any{"field_id": defaultFieldID, "field_title": primaryTitle, "field_type": "FIELD_TYPE_TEXT"}}})
	if err != nil || apiError(response) != nil {
		return fmt.Errorf("默认主字段改名失败")
	}
	fieldsResponse, err = client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
	if err != nil || apiError(fieldsResponse) != nil {
		return fmt.Errorf("默认主字段改名后回读失败")
	}
	fields = resultSlice(fieldsResponse, "fields")
	if len(fields) != 1 {
		return fmt.Errorf("默认主字段改名后数量不一致")
	}
	verified, _ := fields[0].(map[string]any)
	if verified["field_id"] != defaultFieldID || verified["field_title"] != primaryTitle || verified["field_type"] != "FIELD_TYPE_TEXT" {
		return fmt.Errorf("默认主字段改名后回读未收敛")
	}
	return nil
}

func initializeRecordValuesEmpty(values map[string]any) bool {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
		case string:
			if strings.TrimSpace(typed) != "" {
				return false
			}
		case []any:
			if len(typed) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func addMissingInitializeFields(ctx context.Context, client wecomRequester, documentID, sheetID string, role zoopschema.Role, roleSheetIDs map[string]string, references bool) error {
	response, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": sheetID})
	if err != nil || apiError(response) != nil {
		return fmt.Errorf("%s 字段回读失败", role.Role)
	}
	existing, err := mirrorFieldsFromAny(resultSlice(response, "fields"))
	if err != nil {
		return err
	}
	missing := []any{}
	ordered := append([]zoopschema.Field(nil), role.Fields...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Title == role.PrimaryFieldTitle && ordered[j].Title != role.PrimaryFieldTitle
	})
	for _, field := range ordered {
		if (field.Reference != nil) != references {
			continue
		}
		if current, ok := existing[field.Title]; ok {
			if current.Type != field.Type {
				return fmt.Errorf("%s 字段 %s 类型冲突", role.Role, field.Title)
			}
			continue
		}
		if field.UnsupportedForCreate {
			return fmt.Errorf("%s 字段 %s 缺少已验证创建契约", role.Role, field.Title)
		}
		wire, err := initializeFieldWire(ctx, client, documentID, field, roleSheetIDs)
		if err != nil {
			return err
		}
		missing = append(missing, wire)
	}
	if len(missing) > 0 {
		result, err := client.Request(ctx, "add_fields", map[string]any{"docid": documentID, "sheet_id": sheetID, "fields": missing})
		if err != nil || apiError(result) != nil {
			return fmt.Errorf("%s 缺失字段新增失败", role.Role)
		}
	}
	return nil
}

func initializeFieldWire(ctx context.Context, client wecomRequester, documentID string, field zoopschema.Field, roleSheetIDs map[string]string) (map[string]any, error) {
	result := map[string]any{"field_title": field.Title, "field_type": field.Type}
	switch field.Type {
	case "FIELD_TYPE_TEXT":
	case "FIELD_TYPE_NUMBER":
		result["property_number"] = map[string]any{}
	case "FIELD_TYPE_CHECKBOX":
		result["property_checkbox"] = map[string]any{"checked": false}
	case "FIELD_TYPE_DATE_TIME":
		result["property_date_time"] = map[string]any{"auto_fill": false, "format": "yyyy-mm-dd hh:mm"}
	case "FIELD_TYPE_URL":
		result["property_url"] = map[string]any{"type": "LINK_TYPE_PURE_TEXT"}
	case "FIELD_TYPE_USER":
		result["property_user"] = map[string]any{"is_multiple": false, "is_notified": false}
	case "FIELD_TYPE_SINGLE_SELECT":
		options := []any{}
		for _, label := range field.Options {
			options = append(options, map[string]any{"text": label})
		}
		result["property_single_select"] = map[string]any{"is_quick_add": false, "options": options}
	case "FIELD_TYPE_REFERENCE":
		if field.Reference == nil || roleSheetIDs[field.Reference.Role] == "" {
			return nil, fmt.Errorf("关联字段 %s 逻辑目标无效", field.Title)
		}
		response, err := client.Request(ctx, "get_fields", map[string]any{"docid": documentID, "sheet_id": roleSheetIDs[field.Reference.Role]})
		if err != nil || apiError(response) != nil {
			return nil, fmt.Errorf("关联字段 %s 目标回读失败", field.Title)
		}
		targets, err := mirrorFieldsFromAny(resultSlice(response, "fields"))
		if err != nil {
			return nil, err
		}
		target := targets[field.Reference.FieldTitle]
		if target.ID == "" {
			return nil, fmt.Errorf("关联字段 %s 目标主字段不存在", field.Title)
		}
		result["property_reference"] = map[string]any{"sub_id": roleSheetIDs[field.Reference.Role], "field_id": target.ID, "is_multiple": field.Reference.Multiple}
	default:
		return nil, fmt.Errorf("字段 %s 类型 %s 缺少已验证创建契约", field.Title, field.Type)
	}
	return result, nil
}

func addInitializeActiveRow(ctx context.Context, client wecomRequester, registryID, registrySheetID string, fields map[string]config.Field, runtime config.Config, businessID, schemaVersion string) error {
	values := map[string]any{}
	for title, value := range map[string]string{
		"registry_key": runtime.RegistryKey, "docid": businessID, "lifecycle_status": "active", "name": "Zoop｜" + runtime.RegistryKey,
		"document_role": "zoop_business", "schema_version": schemaVersion, "mcp_source": runtime.InstanceName, "type": "smartsheet",
	} {
		field := fields[title]
		if field.ID == "" {
			return fmt.Errorf("Registry 缺少 active row 字段 %s", title)
		}
		values[field.ID] = []any{map[string]any{"type": "text", "text": value}}
	}
	response, err := client.Request(ctx, "add_records", map[string]any{"docid": registryID, "sheet_id": registrySheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "records": []any{map[string]any{"values": values}}})
	if err != nil || apiError(response) != nil {
		records, complete, readErr := readAllInitializeRecords(ctx, client, registryID, registrySheetID)
		if readErr == nil && complete {
			resolved, count := initializeActiveBusiness(records, fields, runtime.RegistryKey)
			if count == 1 && resolved == businessID {
				return nil
			}
		}
		return fmt.Errorf("Registry active row 写入结果不确定且未能回读收敛")
	}
	records, complete, err := readAllInitializeRecords(ctx, client, registryID, registrySheetID)
	if err != nil || !complete {
		return fmt.Errorf("Registry active row 写后完整回读失败")
	}
	resolved, count := initializeActiveBusiness(records, fields, runtime.RegistryKey)
	if count != 1 || resolved != businessID {
		return fmt.Errorf("Registry active row 写后唯一性未通过")
	}
	return nil
}

func observeInstanceInitialization(ctx context.Context, runtime config.Config, client wecomRequester, clientErr error, input initializeStatusInput) initializeObservation {
	catalog, catalogErr := zoopschema.Current()
	return observeInstanceInitializationWithCatalog(ctx, runtime, client, clientErr, input, catalog, catalogErr)
}

func observeInstanceInitializationWithCatalog(ctx context.Context, runtime config.Config, client wecomRequester, clientErr error, input initializeStatusInput, catalog zoopschema.Catalog, catalogErr error) initializeObservation {
	observation := initializeObservation{State: "changes_planned", FieldCounts: map[string]int{}}
	snapshot := initializeSnapshot{
		InstanceName: runtime.InstanceName, ConfigDigest: runtime.Digest(), RoleSheetIDs: map[string]string{}, RoleFieldsDigests: map[string]string{},
		InputRegistryDocumentID: input.RegistryDocumentID, RecoveryRegistryInput: input.RecoveryRegistryDocumentID, RecoveryBusinessInput: input.RecoveryBusinessDocumentID,
	}
	if catalogErr != nil {
		observation.State = "conflict"
		observation.Conflicts = append(observation.Conflicts, "catalog_invalid")
		observation.Snapshot = snapshot
		return observation
	}
	snapshot.CatalogVersion = catalog.Version
	snapshot.CatalogCreationComplete = catalog.CompleteForCreation
	snapshot.CatalogDigest = digestValue(catalog)

	journal, journalDigest, journalExists, journalErr := loadInstanceInitializeJournal(instanceInitializeJournalPath(runtime))
	snapshot.JournalDigest = journalDigest
	if journalErr != nil {
		observation.Conflicts = append(observation.Conflicts, "journal_invalid")
	}
	if journalExists {
		observation.JournalPhase = journal.Phase
		if journal.Phase == "recovery_required" {
			observation.State = "recovery_required"
			observation.RecoveryAssetKind = journal.AssetKind
			observation.RecoveryOperation = journal.OperationID
		}
	}

	registryDocumentID := runtime.RegistryDocumentID
	legacy, legacyExists, legacyErr := loadRegistryBootstrapState(registryBootstrapStatePath(runtime))
	if legacyErr != nil {
		observation.Conflicts = append(observation.Conflicts, "legacy_registry_sentinel_invalid")
	}
	if registryDocumentID == "" && journal.RegistryDocumentID != "" {
		registryDocumentID = journal.RegistryDocumentID
	}
	if registryDocumentID == "" && legacyExists && legacy.DocumentID != "" {
		registryDocumentID = legacy.DocumentID
	}
	registryRecoveryAllowed := (journalExists && journal.Phase == "recovery_required" && journal.AssetKind == "registry") || (legacyExists && legacy.Phase == "creating" && legacy.DocumentID == "")
	if registryRecoveryAllowed && observation.RecoveryAssetKind == "" {
		observation.RecoveryAssetKind = "registry"
		observation.RecoveryOperation = "legacy-registry-bootstrap"
	}
	if input.RegistryDocumentID != "" {
		if registryRecoveryAllowed {
			observation.Conflicts = append(observation.Conflicts, "registry_import_during_recovery")
		} else if registryDocumentID != "" && registryDocumentID != input.RegistryDocumentID {
			observation.Conflicts = append(observation.Conflicts, "registry_import_id_conflict")
		} else {
			registryDocumentID = input.RegistryDocumentID
		}
	}
	if input.RecoveryRegistryDocumentID != "" {
		if !registryRecoveryAllowed {
			observation.Conflicts = append(observation.Conflicts, "registry_recovery_without_matching_sentinel")
		} else if registryDocumentID != "" && registryDocumentID != input.RecoveryRegistryDocumentID {
			observation.Conflicts = append(observation.Conflicts, "registry_recovery_id_conflict")
		} else {
			registryDocumentID = input.RecoveryRegistryDocumentID
		}
	}
	snapshot.RegistryOwnedByJournal = journalExists && journal.RegistryOwned && journal.RegistryDocumentID != "" && journal.RegistryDocumentID == registryDocumentID
	businessRecoveryAllowed := journalExists && journal.Phase == "recovery_required" && journal.AssetKind == "business"
	businessRecoveryDocumentID := ""
	if businessRecoveryAllowed {
		businessRecoveryDocumentID = journal.BusinessDocumentID
	}
	if input.RecoveryBusinessDocumentID != "" {
		if !businessRecoveryAllowed {
			observation.Conflicts = append(observation.Conflicts, "business_recovery_without_matching_sentinel")
		} else if businessRecoveryDocumentID != "" && businessRecoveryDocumentID != input.RecoveryBusinessDocumentID {
			observation.Conflicts = append(observation.Conflicts, "business_recovery_id_conflict")
		} else {
			businessRecoveryDocumentID = input.RecoveryBusinessDocumentID
		}
	}
	snapshot.BusinessOwnedByJournal = journalExists && journal.BusinessOwned && journal.BusinessDocumentID != "" && journal.BusinessDocumentID == businessRecoveryDocumentID
	capabilityMissing := false
	for _, operation := range []string{"get_doc_base_info", "get_doc_auth", "get_sheet", "get_fields", "get_records"} {
		if !runtime.AllowsInGroup(instanceInitializeGroup, operation) {
			observation.Conflicts = append(observation.Conflicts, "instance_initialize_capability_missing:"+operation)
			capabilityMissing = true
		}
	}
	snapshot.RegistryDocumentID = registryDocumentID
	if registryDocumentID == "" {
		if registryRecoveryAllowed {
			observation.State = "recovery_required"
			observation.PlannedOperations = append(observation.PlannedOperations, "bind_recovery_registry_document_id")
		} else {
			observation.PlannedOperations = append(observation.PlannedOperations, "create_registry")
		}
		if capabilityMissing || clientErr != nil || client == nil {
			observation.State = "environment_unavailable"
			if clientErr != nil || client == nil {
				observation.Conflicts = append(observation.Conflicts, "gnas_environment_unavailable")
			}
		} else if !registryRecoveryAllowed {
			observation.SnapshotComplete = true
			if !catalog.CompleteForCreation {
				observation.State = "capability_gap"
				observation.Conflicts = append(observation.Conflicts, "catalog_not_complete_for_creation:Z-S01.进度条.formulaModel")
			}
		}
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}

	if capabilityMissing {
		observation.State = "environment_unavailable"
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	if len(observation.Conflicts) > 0 {
		observation.State = "conflict"
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	if clientErr != nil || client == nil {
		observation.State = "environment_unavailable"
		observation.Conflicts = append(observation.Conflicts, "gnas_environment_unavailable")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}

	registryIdentity, registryAuthorization, err := readInitializeDocumentIdentity(ctx, client, registryDocumentID, "SMART_SHEETS_IDS", runtime.SchemaAdminUser)
	if err != nil {
		observation.Conflicts = append(observation.Conflicts, "registry_identity_or_auth_unverified")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	snapshot.RegistryIdentityDigest = digestValue(registryIdentity)
	snapshot.RegistryAuthDigest = digestValue(registryAuthorization)
	registrySheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": registryDocumentID})
	if err != nil || apiError(registrySheets) != nil {
		observation.State = "environment_unavailable"
		observation.Conflicts = append(observation.Conflicts, "registry_unavailable")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	registrySmartSheets := smartSheetIdentities(registrySheets)
	if len(registrySmartSheets) != 1 {
		observation.Conflicts = append(observation.Conflicts, "registry_smartsheet_not_unique")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	registrySheetID := registrySmartSheets[0].ID
	snapshot.RegistrySheetID = registrySheetID
	if runtime.RegistrySheetID != "" && runtime.RegistrySheetID != registrySheetID {
		observation.Conflicts = append(observation.Conflicts, "config_registry_sheet_id_conflict")
	}
	fields, err := registryFieldDefinitions(ctx, client, registryDocumentID, registrySheetID)
	if err != nil {
		observation.Conflicts = append(observation.Conflicts, "registry_fields_unreadable")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	snapshot.RegistryFieldsDigest = digestValue(fields)
	missingRegistryFields := 0
	for _, title := range registryBootstrapFields {
		field, ok := fields[title]
		if !ok {
			missingRegistryFields++
			continue
		}
		if field.Type != "FIELD_TYPE_TEXT" {
			observation.Conflicts = append(observation.Conflicts, "registry_field_type_conflict:"+title)
		}
	}
	if missingRegistryFields > 0 {
		observation.PlannedOperations = append(observation.PlannedOperations, "add_missing_registry_fields")
		observation.SnapshotComplete = true
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}

	records, complete, err := readAllInitializeRecords(ctx, client, registryDocumentID, registrySheetID)
	if err != nil {
		observation.State = "environment_unavailable"
		observation.Conflicts = append(observation.Conflicts, "registry_records_unavailable")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	snapshot.RegistryRecordsComplete = complete
	if !complete {
		observation.Conflicts = append(observation.Conflicts, "registry_pagination_incomplete")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	keyID, docID, lifecycleID := fields["registry_key"].ID, fields["docid"].ID, fields["lifecycle_status"].ID
	activeRowDigests := []string{}
	for _, rawRecord := range records {
		record, _ := rawRecord.(map[string]any)
		values, _ := record["values"].(map[string]any)
		if initializeTextCell(values[keyID]) != runtime.RegistryKey || initializeTextCell(values[lifecycleID]) != "active" {
			continue
		}
		snapshot.ActiveRegistryCount++
		snapshot.ActiveRegistryRecordID, _ = record["record_id"].(string)
		snapshot.BusinessDocumentID = initializeTextCell(values[docID])
		activeRowDigests = append(activeRowDigests, digestValue(values))
	}
	sort.Strings(activeRowDigests)
	snapshot.ActiveRegistryRowsDigest = digestValue(activeRowDigests)
	if snapshot.ActiveRegistryCount > 1 || (snapshot.ActiveRegistryCount == 1 && snapshot.BusinessDocumentID == "") {
		observation.Conflicts = append(observation.Conflicts, "active_registry_row_not_unique")
		observation.SnapshotComplete = true
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	if snapshot.ActiveRegistryCount == 0 {
		if businessRecoveryAllowed {
			observation.State = "recovery_required"
			if businessRecoveryDocumentID == "" {
				observation.PlannedOperations = append(observation.PlannedOperations, "bind_recovery_business_document_id")
				observation.Snapshot = snapshot
				return finalizeInitializeObservation(observation)
			}
			snapshot.BusinessDocumentID = businessRecoveryDocumentID
			observation.PlannedOperations = append(observation.PlannedOperations, "verify_recovered_business_document", "register_unique_active_row")
		} else {
			observation.PlannedOperations = append(observation.PlannedOperations, "resolve_business_document", "reconcile_zoop_nine_tables", "register_unique_active_row", "generate_schema", "commit_local_config", "z_s01_read_only_smoke")
			if !catalog.CompleteForCreation {
				observation.State = "capability_gap"
				observation.Conflicts = append(observation.Conflicts, "catalog_not_complete_for_creation:Z-S01.进度条.formulaModel")
			}
			observation.SnapshotComplete = true
			observation.Snapshot = snapshot
			return finalizeInitializeObservation(observation)
		}
	}
	if input.RecoveryBusinessDocumentID != "" && input.RecoveryBusinessDocumentID != snapshot.BusinessDocumentID {
		observation.Conflicts = append(observation.Conflicts, "business_recovery_id_conflict")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}

	businessIdentity, businessAuthorization, err := readInitializeDocumentIdentity(ctx, client, snapshot.BusinessDocumentID, "", runtime.SchemaAdminUser)
	if err != nil {
		observation.Conflicts = append(observation.Conflicts, "business_identity_or_auth_unverified")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	snapshot.BusinessIdentityDigest = digestValue(businessIdentity)
	snapshot.BusinessAuthDigest = digestValue(businessAuthorization)
	businessSheets, err := client.Request(ctx, "get_sheet", map[string]any{"docid": snapshot.BusinessDocumentID})
	if err != nil || apiError(businessSheets) != nil {
		observation.State = "environment_unavailable"
		observation.Conflicts = append(observation.Conflicts, "business_document_unavailable")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	identities := smartSheetIdentities(businessSheets)
	observedFieldContracts := map[string]map[string]config.Field{}
	allRoleFieldsReadable := true
	for _, expected := range catalog.Roles {
		matches := []sheetIdentity{}
		for _, sheet := range identities {
			if strings.HasPrefix(sheet.Name, expected.Role+"｜") {
				matches = append(matches, sheet)
			}
		}
		if len(matches) > 1 {
			observation.Conflicts = append(observation.Conflicts, "role_sheet_not_unique:"+expected.Role)
			continue
		}
		if len(matches) == 0 {
			observation.PlannedOperations = append(observation.PlannedOperations, "add_role_sheet:"+expected.Role)
			continue
		}
		snapshot.RoleSheetIDs[expected.Role] = matches[0].ID
		response, requestErr := client.Request(ctx, "get_fields", map[string]any{"docid": snapshot.BusinessDocumentID, "sheet_id": matches[0].ID})
		if requestErr != nil || apiError(response) != nil {
			observation.Conflicts = append(observation.Conflicts, "role_fields_unreadable:"+expected.Role)
			allRoleFieldsReadable = false
			continue
		}
		rawFields := resultSlice(response, "fields")
		snapshot.RoleFieldsDigests[expected.Role] = digestValue(rawFields)
		mirroredFields, mirrorErr := mirrorFieldsFromAny(rawFields)
		if mirrorErr != nil {
			observation.Conflicts = append(observation.Conflicts, "role_field_properties_invalid:"+expected.Role)
			allRoleFieldsReadable = false
			continue
		}
		observedFieldContracts[expected.Role] = mirroredFields
		observedFields := map[string]string{}
		for _, rawField := range rawFields {
			field, _ := rawField.(map[string]any)
			title, _ := field["field_title"].(string)
			fieldType, _ := field["field_type"].(string)
			if title == "" || fieldType == "" {
				continue
			}
			if _, duplicate := observedFields[title]; duplicate {
				observation.Conflicts = append(observation.Conflicts, "role_field_not_unique:"+expected.Role+":"+title)
			}
			observedFields[title] = fieldType
		}
		observation.FieldCounts[expected.Role] = len(observedFields)
		for _, field := range expected.Fields {
			observedType, exists := observedFields[field.Title]
			if !exists {
				operation := "add_role_field:" + expected.Role + ":" + field.Title
				observation.PlannedOperations = append(observation.PlannedOperations, operation)
				if field.UnsupportedForCreate {
					observation.State = "capability_gap"
					observation.Conflicts = append(observation.Conflicts, "unsupported_field_missing:"+expected.Role+":"+field.Title)
				}
			} else if observedType != field.Type {
				observation.Conflicts = append(observation.Conflicts, "role_field_type_conflict:"+expected.Role+":"+field.Title)
			}
		}
	}
	observation.SnapshotComplete = allRoleFieldsReadable
	for _, expectedRole := range catalog.Roles {
		observed := observedFieldContracts[expectedRole.Role]
		for _, expectedField := range expectedRole.Fields {
			field, exists := observed[expectedField.Title]
			if !exists || field.Type != expectedField.Type {
				continue
			}
			for _, option := range expectedField.Options {
				if _, ok := field.Options[option]; !ok {
					observation.Conflicts = append(observation.Conflicts, "role_field_option_missing:"+expectedRole.Role+":"+expectedField.Title+":"+option)
				}
			}
			if expectedField.Reference == nil {
				continue
			}
			targetFields := observedFieldContracts[expectedField.Reference.Role]
			targetField := targetFields[expectedField.Reference.FieldTitle]
			targetSheetID := snapshot.RoleSheetIDs[expectedField.Reference.Role]
			if targetSheetID == "" || targetField.ID == "" || field.ReferenceTargetSheetID != targetSheetID || field.ReferenceTargetFieldID != targetField.ID || field.ReferenceIsMultiple == nil || *field.ReferenceIsMultiple != expectedField.Reference.Multiple {
				observation.Conflicts = append(observation.Conflicts, "role_field_reference_conflict:"+expectedRole.Role+":"+expectedField.Title)
			}
		}
	}
	if len(snapshot.RoleSheetIDs) == 9 && len(observation.Conflicts) == 0 && !hasRemoteInitializePlan(observation.PlannedOperations) {
		local, localErr := config.LoadSchema(runtime.SchemaMirrorPath)
		if localErr != nil {
			observation.PlannedOperations = append(observation.PlannedOperations, "generate_schema", "commit_local_config")
		} else {
			snapshot.LocalSchemaDigest = local.Digest
			if differences := compareInitializeSchema(local, observedFieldContracts); len(differences) > 0 {
				observation.PlannedOperations = append(observation.PlannedOperations, "generate_schema", "commit_local_config")
				for _, difference := range differences {
					observation.PlannedOperations = append(observation.PlannedOperations, "refresh_"+difference)
				}
			}
			metadataComplete := runtime.InitializationGeneration != "" && runtime.SchemaVersion != "" && runtime.SchemaDigest != "" && runtime.RegistrySheetID != "" && runtime.InitializedState == "config_committed"
			if !metadataComplete {
				observation.PlannedOperations = append(observation.PlannedOperations, "commit_local_config")
			} else if runtime.SchemaDigest != local.Digest {
				observation.Conflicts = append(observation.Conflicts, "config_schema_digest_conflict")
			}
			if runtime.SchemaVersion != "" && runtime.SchemaVersion != catalog.Version {
				observation.Conflicts = append(observation.Conflicts, "config_schema_version_conflict")
			}
		}
	}
	if len(snapshot.RoleSheetIDs) == 9 && len(observation.Conflicts) == 0 && !hasRemoteInitializePlan(observation.PlannedOperations) {
		smoke, smokeErr := client.Request(ctx, "get_records", map[string]any{"docid": snapshot.BusinessDocumentID, "sheet_id": snapshot.RoleSheetIDs["Z-S01"], "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 1})
		if smokeErr != nil || apiError(smoke) != nil {
			observation.Conflicts = append(observation.Conflicts, "z_s01_smoke_failed")
		} else {
			snapshot.SmokeVerified = true
			observation.SmokeRecordCount = len(resultSlice(smoke, "records"))
		}
	}
	observation.Snapshot = snapshot
	return finalizeInitializeObservation(observation)
}

func finalizeInitializeObservation(observation initializeObservation) initializeObservation {
	sort.Strings(observation.Invariants)
	sort.Strings(observation.Conflicts)
	sort.Strings(observation.PlannedOperations)
	if len(observation.Conflicts) > 0 && observation.State != "environment_unavailable" && observation.State != "recovery_required" && observation.State != "capability_gap" {
		observation.State = "conflict"
	} else if len(observation.Conflicts) == 0 && len(observation.PlannedOperations) == 0 && observation.Snapshot.SmokeVerified {
		observation.State = "ready"
	}
	if observation.Snapshot.RegistryRecordsComplete {
		observation.Invariants = append(observation.Invariants, "registry_records_fully_paginated")
	}
	if observation.Snapshot.ActiveRegistryCount == 1 {
		observation.Invariants = append(observation.Invariants, "exactly_one_active_registry_row")
	}
	if len(observation.Snapshot.RoleSheetIDs) == 9 {
		observation.Invariants = append(observation.Invariants, "z_s01_to_z_s09_unique")
	}
	return observation
}

func publicInitializeObservation(observation initializeObservation) map[string]any {
	return map[string]any{
		"registry_document_resolved": observation.Snapshot.RegistryDocumentID != "",
		"registry_sheet_resolved":    observation.Snapshot.RegistrySheetID != "",
		"registry_field_count":       len(registryBootstrapFields),
		"registry_records_complete":  observation.Snapshot.RegistryRecordsComplete,
		"active_registry_row_count":  observation.Snapshot.ActiveRegistryCount,
		"business_document_resolved": observation.Snapshot.BusinessDocumentID != "",
		"role_sheet_count":           len(observation.Snapshot.RoleSheetIDs),
		"role_field_counts":          observation.FieldCounts,
		"local_schema_loaded":        observation.Snapshot.LocalSchemaDigest != "",
		"z_s01_smoke_verified":       observation.Snapshot.SmokeVerified,
		"z_s01_smoke_record_count":   observation.SmokeRecordCount,
	}
}

type sheetIdentity struct{ ID, Name string }

func smartSheetIdentities(response map[string]any) []sheetIdentity {
	result := []sheetIdentity{}
	for _, raw := range resultSlice(response, "sheet_list") {
		sheet, _ := raw.(map[string]any)
		if sheet["type"] != "smartsheet" {
			continue
		}
		id, _ := sheet["sheet_id"].(string)
		name, _ := sheetNameForInitialize(sheet)
		if id != "" {
			result = append(result, sheetIdentity{ID: id, Name: name})
		}
	}
	return result
}

func sheetNameForInitialize(sheet map[string]any) (string, bool) {
	for _, key := range []string{"name", "sheet_name", "title"} {
		if value, ok := sheet[key].(string); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

func readInitializeDocumentIdentity(ctx context.Context, client wecomRequester, documentID, expectedName, expectedManagerUser string) (map[string]any, map[string]any, error) {
	base, err := client.Request(ctx, "get_doc_base_info", map[string]any{"docid": documentID})
	if err != nil || apiError(base) != nil {
		return nil, nil, fmt.Errorf("document base info unavailable")
	}
	baseResult, _ := base["result"].(map[string]any)
	docType, name := findInitializeDocumentMetadata(baseResult, 0)
	if docType != 10 || name == "" || expectedName != "" && name != expectedName {
		return nil, nil, fmt.Errorf("document identity did not match")
	}
	authorization, err := client.Request(ctx, "get_doc_auth", map[string]any{"docid": documentID})
	if err != nil || apiError(authorization) != nil {
		return nil, nil, fmt.Errorf("document authorization unavailable")
	}
	authResult, _ := authorization["result"].(map[string]any)
	managementProof, err := verifyInitializeManagementAuthorization(authResult, expectedManagerUser)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"doc_type": docType, "name_digest": digestValue(name), "expected_name_matched": expectedName == "" || name == expectedName}, managementProof, nil
}

// verifyInitializeManagementAuthorization follows the response contract for
// Enterprise WeCom's “获取文档权限信息” endpoint (official document path 97461).
// That endpoint is restricted to documents created by the calling application;
// a structurally complete successful response therefore proves that the same
// application owns the API management plane for this document. In addition,
// the configured schema_admin_user must appear in doc_member_list with the
// documented manager permission value 7. Arbitrary fields such as
// auth_type=admin are deliberately not accepted.
func verifyInitializeManagementAuthorization(auth map[string]any, expectedManagerUser string) (map[string]any, error) {
	if expectedManagerUser == "" {
		return nil, fmt.Errorf("document manager identity is not configured")
	}
	accessRule, accessOK := auth["access_rule"].(map[string]any)
	secureSetting, secureOK := auth["secure_setting"].(map[string]any)
	members, membersOK := auth["doc_member_list"].([]any)
	departments, departmentsOK := auth["co_auth_list"].([]any)
	if !accessOK || len(accessRule) == 0 || !secureOK || len(secureSetting) == 0 || !membersOK || !departmentsOK {
		return nil, fmt.Errorf("document management permission unproven")
	}
	managerMatched := false
	for _, rawMember := range members {
		member, ok := rawMember.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("document management permission response invalid")
		}
		userID, _ := member["userid"].(string)
		authValue, ok := initializeInteger(member["auth"])
		if userID == expectedManagerUser && ok && authValue == 7 {
			managerMatched = true
		}
	}
	if !managerMatched {
		return nil, fmt.Errorf("configured document manager permission unproven")
	}
	return map[string]any{
		"application_document_management_proven": true,
		"configured_manager_proven":              true,
		"access_rule_present":                    true,
		"secure_setting_present":                 true,
		"document_member_count":                  len(members),
		"department_rule_count":                  len(departments),
	}, nil
}

func initializeInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := intFromJSONNumber(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func hasRemoteInitializePlan(operations []string) bool {
	for _, operation := range operations {
		if operation != "generate_schema" && operation != "commit_local_config" && operation != "z_s01_read_only_smoke" && !strings.HasPrefix(operation, "refresh_local_schema_") {
			return true
		}
	}
	return false
}

func requiresCompleteInitializeCatalog(operations []string) bool {
	for _, operation := range operations {
		if operation == "create_registry" || operation == "resolve_business_document" || operation == "reconcile_zoop_nine_tables" || strings.HasPrefix(operation, "add_role_sheet:") {
			return true
		}
	}
	return false
}

func compareInitializeSchema(local config.Schema, observed map[string]map[string]config.Field) []string {
	differences := []string{}
	for index := 1; index <= 9; index++ {
		role := fmt.Sprintf("Z-S%02d", index)
		localFields := local.Roles[role]
		onlineFields := observed[role]
		if len(localFields) != len(onlineFields) {
			differences = append(differences, "local_schema_field_count_stale:"+role)
		}
		for title, online := range onlineFields {
			stored, exists := localFields[title]
			if !exists || stored.ID != online.ID || stored.Type != online.Type || !equalInitializeOptions(stored.Options, online.Options) || stored.ReferenceTargetSheetID != online.ReferenceTargetSheetID || stored.ReferenceTargetFieldID != online.ReferenceTargetFieldID || !equalInitializeMultiple(stored.ReferenceIsMultiple, online.ReferenceIsMultiple) {
				differences = append(differences, "local_schema_field_stale:"+role+":"+title)
			}
		}
	}
	sort.Strings(differences)
	return differences
}

func equalInitializeOptions(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for label, id := range left {
		if right[label] != id {
			return false
		}
	}
	return true
}

func equalInitializeMultiple(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func findInitializeDocumentMetadata(value any, depth int) (int, string) {
	if depth > 4 {
		return 0, ""
	}
	object, ok := value.(map[string]any)
	if !ok {
		return 0, ""
	}
	docType := 0
	switch typed := object["doc_type"].(type) {
	case float64:
		docType = int(typed)
	case int:
		docType = typed
	case json.Number:
		docType, _ = intFromJSONNumber(typed)
	case string:
		_, _ = fmt.Sscanf(typed, "%d", &docType)
	}
	name := ""
	for _, key := range []string{"doc_name", "name", "title"} {
		if candidate, ok := object[key].(string); ok && strings.TrimSpace(candidate) != "" {
			name = strings.TrimSpace(candidate)
			break
		}
	}
	if docType != 0 && name != "" {
		return docType, name
	}
	for _, nested := range object {
		if nestedType, nestedName := findInitializeDocumentMetadata(nested, depth+1); nestedType != 0 && nestedName != "" {
			return nestedType, nestedName
		}
	}
	return docType, name
}

func intFromJSONNumber(value json.Number) (int, error) {
	parsed, err := value.Int64()
	return int(parsed), err
}

func readAllInitializeRecords(ctx context.Context, client wecomRequester, documentID, sheetID string) ([]any, bool, error) {
	const pageSize = 200
	all := []any{}
	for offset := 0; offset < 100000; offset += pageSize {
		response, err := client.Request(ctx, "get_records", map[string]any{"docid": documentID, "sheet_id": sheetID, "key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "offset": offset, "limit": pageSize})
		if err != nil {
			return nil, false, err
		}
		if err := apiError(response); err != nil {
			return nil, false, err
		}
		items := resultSlice(response, "records")
		all = append(all, items...)
		result, _ := response["result"].(map[string]any)
		hasMore, _ := result["has_more"].(bool)
		cursor, _ := result["next_cursor"].(string)
		if !hasMore && cursor == "" && len(items) < pageSize {
			return all, true, nil
		}
		if len(items) == 0 {
			return all, false, nil
		}
	}
	return all, false, nil
}

func initializeTextCell(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if text, ok := item["text"].(string); ok {
			return text
		}
	}
	return ""
}

func mirrorFieldsFromAny(items []any) (map[string]config.Field, error) {
	raw := make([]map[string]any, 0, len(items))
	for _, item := range items {
		field, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field is not an object")
		}
		raw = append(raw, field)
	}
	fields, err := mirrorFields(raw)
	if err != nil {
		return nil, err
	}
	result := make(map[string]config.Field, len(fields))
	for _, field := range fields {
		if _, duplicate := result[field.Title]; duplicate {
			return nil, fmt.Errorf("field title duplicated")
		}
		result[field.Title] = field
	}
	return result, nil
}

func digestValue(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func initializeObservationDigest(observation initializeObservation) string {
	return digestValue(map[string]any{
		"snapshot": observation.Snapshot, "state": observation.State, "snapshot_complete": observation.SnapshotComplete,
		"conflicts": observation.Conflicts, "planned_operations": observation.PlannedOperations,
	})
}

func instanceInitializeJournalPath(runtime config.Config) string {
	return runtime.StatePath + ".instance-initialize.json"
}

func loadInstanceInitializeJournal(path string) (instanceInitializeJournal, string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return instanceInitializeJournal{}, digestValue("absent"), false, nil
	}
	if err != nil {
		return instanceInitializeJournal{}, "", false, err
	}
	digest := digestValue(json.RawMessage(data))
	var journal instanceInitializeJournal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || decoder.Decode(&struct{}{}) != io.EOF || validateInstanceInitializeJournal(journal) != nil {
		return instanceInitializeJournal{}, digest, false, fmt.Errorf("实例初始化 journal 无效")
	}
	return journal, digest, true, nil
}

func saveInstanceInitializeJournal(path string, journal instanceInitializeJournal) error {
	if err := validateInstanceInitializeJournal(journal); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return config.WriteProtectedFileAtomic(path, data, 0600)
}

func validateInstanceInitializeJournal(journal instanceInitializeJournal) error {
	if journal.Version != instanceInitializeJournalV1 {
		return fmt.Errorf("实例初始化 journal version 无效")
	}
	if _, ok := initializeJournalPhases[journal.Phase]; !ok {
		return fmt.Errorf("实例初始化 journal phase 无效")
	}
	if _, err := time.Parse(time.RFC3339Nano, journal.UpdatedAt); err != nil {
		return fmt.Errorf("实例初始化 journal updated_at 无效")
	}
	for _, documentID := range []string{journal.RegistryDocumentID, journal.BusinessDocumentID} {
		if documentID != "" && !initializeIdentifier.MatchString(documentID) {
			return fmt.Errorf("实例初始化 journal document id 无效")
		}
	}
	if journal.Phase == "recovery_required" && journal.AssetKind != "registry" && journal.AssetKind != "business" {
		return fmt.Errorf("实例初始化 recovery journal 缺少受控 asset_kind")
	}
	return nil
}
