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
	RegistryIdentityDigest   string            `json:"registry_identity_digest"`
	RegistryAuthDigest       string            `json:"registry_auth_digest"`
	RegistrySheetID          string            `json:"registry_sheet_id"`
	RegistryFieldsDigest     string            `json:"registry_fields_digest"`
	RegistryRecordsComplete  bool              `json:"registry_records_complete"`
	ActiveRegistryCount      int               `json:"active_registry_count"`
	ActiveRegistryRecordID   string            `json:"active_registry_record_id"`
	ActiveRegistryRowsDigest string            `json:"active_registry_rows_digest"`
	BusinessDocumentID       string            `json:"business_document_id"`
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
	observation := observeInstanceInitialization(ctx, runtime, client, clientErr, input)
	snapshotDigest := initializeObservationDigest(observation)
	expiresAt := time.Now().UTC().Add(instanceInitializePreviewTTL)
	expiresAtText := ""
	previewID := ""
	if observation.SnapshotComplete && (observation.State == "ready" || observation.State == "changes_planned") {
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
		nextRequiredInput = "recovery_" + observation.RecoveryAssetKind + "_document_id"
	}

	return map[string]any{
		"state":                      observation.State,
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
	observation := observeInstanceInitialization(ctx, runtime, client, clientErr, initializeStatusInput{
		RegistryDocumentID:         input.RegistryDocumentID,
		RecoveryRegistryDocumentID: input.RecoveryRegistryDocumentID,
		RecoveryBusinessDocumentID: input.RecoveryBusinessDocumentID,
	})
	if initializeObservationDigest(observation) != preview.SnapshotDigest {
		return nil, fmt.Errorf("实例初始化预览已失效，请重新执行只读 status")
	}
	// Architecture Gate 1 deliberately keeps every write path unreachable in
	// this candidate. Registering the schema lets clients integrate and test the
	// authorization/preview contract without creating any remote asset.
	return nil, fmt.Errorf("实例初始化 apply 尚未通过 Architecture Gate 1；本候选只提供只读观察和 dry-run，未执行任何写入")
}

func observeInstanceInitialization(ctx context.Context, runtime config.Config, client wecomRequester, clientErr error, input initializeStatusInput) initializeObservation {
	catalog, catalogErr := zoopschema.Current()
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
	if input.RecoveryBusinessDocumentID != "" && !(journalExists && journal.Phase == "recovery_required" && journal.AssetKind == "business") {
		observation.Conflicts = append(observation.Conflicts, "business_recovery_without_matching_sentinel")
	}
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

	registryIdentity, registryAuthorization, err := readInitializeDocumentIdentity(ctx, client, registryDocumentID, "SMART_SHEETS_IDS")
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
		observation.PlannedOperations = append(observation.PlannedOperations, "resolve_business_document", "reconcile_zoop_nine_tables", "register_unique_active_row", "generate_schema", "commit_local_config", "z_s01_read_only_smoke")
		if !catalog.CompleteForCreation {
			observation.Conflicts = append(observation.Conflicts, "catalog_not_complete_for_creation")
		}
		observation.SnapshotComplete = true
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}
	if input.RecoveryBusinessDocumentID != "" && input.RecoveryBusinessDocumentID != snapshot.BusinessDocumentID {
		observation.Conflicts = append(observation.Conflicts, "business_recovery_id_conflict")
		observation.Snapshot = snapshot
		return finalizeInitializeObservation(observation)
	}

	businessIdentity, businessAuthorization, err := readInitializeDocumentIdentity(ctx, client, snapshot.BusinessDocumentID, "")
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
	if len(snapshot.RoleSheetIDs) == 9 && len(observation.Conflicts) == 0 && len(observation.PlannedOperations) == 0 {
		local, localErr := config.LoadSchema(runtime.SchemaMirrorPath)
		if localErr != nil {
			observation.PlannedOperations = append(observation.PlannedOperations, "generate_schema", "commit_local_config")
		} else {
			snapshot.LocalSchemaDigest = local.Digest
			if runtime.SchemaDigest != "" && runtime.SchemaDigest != local.Digest {
				observation.Conflicts = append(observation.Conflicts, "config_schema_digest_conflict")
			}
			if runtime.SchemaVersion != "" && runtime.SchemaVersion != catalog.Version {
				observation.Conflicts = append(observation.Conflicts, "config_schema_version_conflict")
			}
		}
	}
	if len(snapshot.RoleSheetIDs) == 9 && len(observation.Conflicts) == 0 && len(observation.PlannedOperations) == 0 {
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
	if len(observation.Conflicts) > 0 && observation.State != "environment_unavailable" && observation.State != "recovery_required" {
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
		"registry_document_id":       observation.Snapshot.RegistryDocumentID,
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

func readInitializeDocumentIdentity(ctx context.Context, client wecomRequester, documentID, expectedName string) (map[string]any, map[string]any, error) {
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
	authProperties := 0
	for key := range authResult {
		if key != "errcode" && key != "errmsg" {
			authProperties++
		}
	}
	if authProperties == 0 {
		return nil, nil, fmt.Errorf("document authorization incomplete")
	}
	return map[string]any{"doc_type": docType, "name_digest": digestValue(name), "expected_name_matched": expectedName == "" || name == expectedName}, authResult, nil
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
