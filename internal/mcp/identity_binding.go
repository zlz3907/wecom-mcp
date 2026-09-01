package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

const (
	identityBindingArgument      = "identity_binding_id"
	identityBindingSecretEnv     = "TEAM_MCP_AUDIT_HMAC_KEY"
	identityBindingMaxAttempts   = 5
	identityBindingStateVersion  = 1
	identityBindingMessagePrefix = "gmzoop 身份绑定验证码："
)

type verifiedIdentity struct {
	UserID          string
	DisplayName     string
	SubjectRecordID string
}

type verifiedIdentityContextKey struct{}

type identityBindingState struct {
	Version  int                             `json:"version"`
	Bindings map[string]identityBindingEntry `json:"bindings"`
}

type identityBindingEntry struct {
	ActiveUserID           string `json:"active_userid,omitempty"`
	ActiveDisplayName      string `json:"active_display_name,omitempty"`
	ActiveSubjectRecordID  string `json:"active_subject_record_id,omitempty"`
	PendingUserID          string `json:"pending_userid,omitempty"`
	PendingDisplayName     string `json:"pending_display_name,omitempty"`
	PendingSubjectRecordID string `json:"pending_subject_record_id,omitempty"`
	PendingCodeDigest      string `json:"pending_code_digest,omitempty"`
	PendingIdempotencyKey  string `json:"pending_idempotency_key,omitempty"`
	DeliveryState          string `json:"delivery_state,omitempty"`
	FailedAttempts         int    `json:"failed_attempts,omitempty"`
	Generation             int    `json:"generation"`
	UpdatedAt              string `json:"updated_at"`
}

type identityBindingStartInput struct {
	Name             string `json:"name"`
	IdempotencyKey   string `json:"idempotency_key"`
	CurrentBindingID string `json:"current_binding_id"`
}

func identityBindingStartToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "idempotency_key"},
		"properties": map[string]any{
			"name":               map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"idempotency_key":    map[string]any{"type": "string", "minLength": 16, "maxLength": 256},
			"current_binding_id": map[string]any{"type": "string", "minLength": 32, "maxLength": 128},
		},
	}
}

func identityBindingConfirmToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"identity_binding_id", "verification_code"},
		"properties": map[string]any{
			"identity_binding_id": map[string]any{"type": "string", "minLength": 32, "maxLength": 128},
			"verification_code":   map[string]any{"type": "string", "pattern": "^[0-9]{6}$"},
		},
	}
}

func identityBindingStatusToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"identity_binding_id"},
		"properties": map[string]any{
			"identity_binding_id": map[string]any{"type": "string", "minLength": 32, "maxLength": 128},
		},
	}
}

func identityBindingSecret() ([]byte, error) {
	secret := []byte(os.Getenv(identityBindingSecretEnv))
	if len(secret) < 32 {
		return nil, fmt.Errorf("身份绑定密钥未配置")
	}
	return secret, nil
}

func identityBindingStatePath(runtime config.Config) string {
	return runtime.StatePath + ".identity-bindings.json"
}

func identityBindingID(secret []byte, runtime config.Config, idempotencyKey string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("gmzoop-identity-binding-id-v1\x00"))
	mac.Write([]byte(runtime.InstanceName))
	mac.Write([]byte{0})
	mac.Write([]byte(idempotencyKey))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func identityBindingLookupKey(bindingID string) string {
	sum := sha256.Sum256([]byte(bindingID))
	return hex.EncodeToString(sum[:])
}

func identityVerificationCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("无法生成身份验证码")
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func identityVerificationDigest(secret []byte, bindingID, code string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("gmzoop-identity-verification-code-v1\x00"))
	mac.Write([]byte(bindingID))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) startIdentityBinding(ctx context.Context, runtime config.Config, client wecomRequester, raw json.RawMessage) (any, error) {
	var input identityBindingStartInput
	if err := strictDecode(raw, &input, "name", "idempotency_key", "current_binding_id"); err != nil {
		return nil, err
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len([]byte(input.Name)) > 128 || len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 {
		return nil, fmt.Errorf("姓名或 idempotency_key 无效")
	}
	if !runtime.AllowsInGroup(appMessageCapabilityGroup, "list_employees") || !runtime.AllowsInGroup(appMessageCapabilityGroup, "send_app_message") {
		return nil, fmt.Errorf("app_message 专用 capability 未完整启用身份验证")
	}
	secret, err := identityBindingSecret()
	if err != nil {
		return nil, err
	}
	resolver := resolveIdentityCandidate
	if s.identityCandidate != nil {
		resolver = s.identityCandidate
	}
	candidate, err := resolver(ctx, runtime, client, input.Name)
	if err != nil {
		return nil, err
	}
	bindingID := strings.TrimSpace(input.CurrentBindingID)
	if bindingID == "" {
		bindingID = identityBindingID(secret, runtime, input.IdempotencyKey)
	}
	lookupKey := identityBindingLookupKey(bindingID)
	statePath := identityBindingStatePath(runtime)

	code, err := identityVerificationCode()
	if err != nil {
		return nil, err
	}
	codeDigest := identityVerificationDigest(secret, bindingID, code)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s.stateMu.Lock()
	release, err := acquireStateFileLock(statePath)
	if err != nil {
		s.stateMu.Unlock()
		return nil, fmt.Errorf("获取身份绑定状态锁失败")
	}
	state, err := loadIdentityBindingState(statePath)
	if err != nil {
		release()
		s.stateMu.Unlock()
		return nil, err
	}
	entry, exists := state.Bindings[lookupKey]
	if input.CurrentBindingID != "" && (!exists || entry.ActiveUserID == "") {
		release()
		s.stateMu.Unlock()
		return nil, fmt.Errorf("current_binding_id 未绑定有效身份")
	}
	if input.CurrentBindingID == "" && exists && entry.ActiveUserID != "" {
		release()
		s.stateMu.Unlock()
		return nil, fmt.Errorf("idempotency_key 已完成过身份绑定；换绑必须提供 current_binding_id 和新的 idempotency_key")
	}
	if exists && entry.PendingIdempotencyKey == input.IdempotencyKey {
		if entry.PendingUserID != candidate.UserID || entry.PendingSubjectRecordID != candidate.SubjectRecordID {
			release()
			s.stateMu.Unlock()
			return nil, fmt.Errorf("idempotency_key 已绑定其他身份验证")
		}
		release()
		s.stateMu.Unlock()
		return identityBindingStartResult(bindingID, entry, true), nil
	}
	entry.PendingUserID = candidate.UserID
	entry.PendingDisplayName = candidate.DisplayName
	entry.PendingSubjectRecordID = candidate.SubjectRecordID
	entry.PendingCodeDigest = codeDigest
	entry.PendingIdempotencyKey = input.IdempotencyKey
	entry.DeliveryState = "pending"
	entry.FailedAttempts = 0
	entry.UpdatedAt = now
	state.Bindings[lookupKey] = entry
	if err := saveIdentityBindingState(statePath, state); err != nil {
		release()
		s.stateMu.Unlock()
		return nil, err
	}
	release()
	s.stateMu.Unlock()

	response, requestErr := client.Request(ctx, "send_app_message", map[string]any{
		"touser":                   candidate.UserID,
		"msgtype":                  "text",
		"text":                     map[string]string{"content": identityBindingMessagePrefix + code + "。请回到 WorkBuddy 回复此验证码。验证码仅可使用一次，最多允许输错 5 次；如非本人操作请忽略。"},
		"safe":                     0,
		"enable_duplicate_check":   1,
		"duplicate_check_interval": 1800,
	})
	deliveryState := "uncertain"
	if requestErr == nil {
		if _, receiptErr := verifiedMessageReceipt(response); receiptErr == nil {
			deliveryState = "sent"
		} else {
			deliveryState = "failed"
		}
	}
	if updateErr := s.updateIdentityBindingDelivery(statePath, lookupKey, input.IdempotencyKey, deliveryState); updateErr != nil {
		return map[string]any{
			"state":               "verification_delivery_state_pending",
			"identity_binding_id": bindingID,
			"matched_name":        candidate.DisplayName,
			"next_action":         "请填写企业微信中收到的 6 位验证码",
		}, nil
	}
	if deliveryState != "sent" {
		return map[string]any{
			"state":               "verification_delivery_" + deliveryState,
			"identity_binding_id": bindingID,
			"matched_name":        candidate.DisplayName,
			"next_action":         "不要盲目重试同一 idempotency_key；未收到验证码时使用新的 idempotency_key 重新发起",
		}, nil
	}
	entry.DeliveryState = "sent"
	return identityBindingStartResult(bindingID, entry, false), nil
}

func identityBindingStartResult(bindingID string, entry identityBindingEntry, replay bool) map[string]any {
	state := "verification_code_sent"
	if entry.DeliveryState != "sent" {
		state = "verification_delivery_" + entry.DeliveryState
	}
	return map[string]any{
		"state":               state,
		"identity_binding_id": bindingID,
		"matched_name":        entry.PendingDisplayName,
		"idempotent_replay":   replay,
		"binding_expires":     false,
		"next_action":         "请填写企业微信中收到的 6 位验证码",
	}
}

func (s *Server) confirmIdentityBinding(runtime config.Config, raw json.RawMessage) (any, error) {
	var input struct {
		BindingID        string `json:"identity_binding_id"`
		VerificationCode string `json:"verification_code"`
	}
	if err := strictDecode(raw, &input, "identity_binding_id", "verification_code"); err != nil {
		return nil, err
	}
	if len(input.BindingID) < 32 || len(input.BindingID) > 128 || len(input.VerificationCode) != 6 {
		return nil, fmt.Errorf("identity_binding_id 或验证码无效")
	}
	for _, char := range input.VerificationCode {
		if char < '0' || char > '9' {
			return nil, fmt.Errorf("验证码必须是 6 位数字")
		}
	}
	secret, err := identityBindingSecret()
	if err != nil {
		return nil, err
	}
	statePath := identityBindingStatePath(runtime)
	lookupKey := identityBindingLookupKey(input.BindingID)
	expected := identityVerificationDigest(secret, input.BindingID, input.VerificationCode)

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(statePath)
	if err != nil {
		return nil, fmt.Errorf("获取身份绑定状态锁失败")
	}
	defer release()
	state, err := loadIdentityBindingState(statePath)
	if err != nil {
		return nil, err
	}
	entry, exists := state.Bindings[lookupKey]
	if !exists || entry.PendingCodeDigest == "" || entry.PendingUserID == "" || entry.PendingSubjectRecordID == "" {
		return nil, fmt.Errorf("没有可确认的身份绑定")
	}
	if entry.FailedAttempts >= identityBindingMaxAttempts {
		return nil, fmt.Errorf("验证码已锁定，请重新发起身份绑定")
	}
	if !hmac.Equal([]byte(entry.PendingCodeDigest), []byte(expected)) {
		entry.FailedAttempts++
		entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		state.Bindings[lookupKey] = entry
		if err := saveIdentityBindingState(statePath, state); err != nil {
			return nil, err
		}
		if entry.FailedAttempts >= identityBindingMaxAttempts {
			return nil, fmt.Errorf("验证码错误次数达到上限，请重新发起身份绑定")
		}
		return nil, fmt.Errorf("验证码错误，还可尝试 %d 次", identityBindingMaxAttempts-entry.FailedAttempts)
	}
	entry.ActiveUserID = entry.PendingUserID
	entry.ActiveDisplayName = entry.PendingDisplayName
	entry.ActiveSubjectRecordID = entry.PendingSubjectRecordID
	entry.PendingUserID = ""
	entry.PendingDisplayName = ""
	entry.PendingSubjectRecordID = ""
	entry.PendingCodeDigest = ""
	entry.PendingIdempotencyKey = ""
	entry.DeliveryState = ""
	entry.FailedAttempts = 0
	entry.Generation++
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.Bindings[lookupKey] = entry
	if err := saveIdentityBindingState(statePath, state); err != nil {
		return nil, err
	}
	return map[string]any{
		"state":                  "bound",
		"identity_binding_id":    input.BindingID,
		"verified_userid":        entry.ActiveUserID,
		"verified_name":          entry.ActiveDisplayName,
		"zoop_subject_record_id": entry.ActiveSubjectRecordID,
		"binding_generation":     entry.Generation,
		"binding_expires":        false,
		"rebind_supported":       true,
	}, nil
}

func (s *Server) identityBindingStatus(runtime config.Config, raw json.RawMessage) (any, error) {
	var input struct {
		BindingID string `json:"identity_binding_id"`
	}
	if err := strictDecode(raw, &input, "identity_binding_id"); err != nil {
		return nil, err
	}
	identity, entry, err := s.resolveIdentityBinding(runtime, input.BindingID)
	if err != nil {
		return nil, err
	}
	state := "bound"
	if entry.PendingCodeDigest != "" {
		state = "bound_rebind_pending"
	}
	return map[string]any{
		"state":                  state,
		"verified_userid":        identity.UserID,
		"verified_name":          identity.DisplayName,
		"zoop_subject_record_id": identity.SubjectRecordID,
		"binding_generation":     entry.Generation,
		"binding_expires":        false,
		"rebind_supported":       true,
	}, nil
}

func (s *Server) resolveIdentityBinding(runtime config.Config, bindingID string) (verifiedIdentity, identityBindingEntry, error) {
	bindingID = strings.TrimSpace(bindingID)
	if len(bindingID) < 32 || len(bindingID) > 128 {
		return verifiedIdentity{}, identityBindingEntry{}, fmt.Errorf("identity_binding_id 无效")
	}
	statePath := identityBindingStatePath(runtime)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(statePath)
	if err != nil {
		return verifiedIdentity{}, identityBindingEntry{}, fmt.Errorf("获取身份绑定状态锁失败")
	}
	defer release()
	state, err := loadIdentityBindingState(statePath)
	if err != nil {
		return verifiedIdentity{}, identityBindingEntry{}, err
	}
	entry, exists := state.Bindings[identityBindingLookupKey(bindingID)]
	if !exists || entry.ActiveUserID == "" || entry.ActiveSubjectRecordID == "" {
		return verifiedIdentity{}, identityBindingEntry{}, fmt.Errorf("身份尚未绑定，请先完成姓名与企业微信验证码验证")
	}
	return verifiedIdentity{UserID: entry.ActiveUserID, DisplayName: entry.ActiveDisplayName, SubjectRecordID: entry.ActiveSubjectRecordID}, entry, nil
}

func (s *Server) updateIdentityBindingDelivery(path, lookupKey, idempotencyKey, deliveryState string) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	release, err := acquireStateFileLock(path)
	if err != nil {
		return err
	}
	defer release()
	state, err := loadIdentityBindingState(path)
	if err != nil {
		return err
	}
	entry, exists := state.Bindings[lookupKey]
	if !exists || entry.PendingIdempotencyKey != idempotencyKey {
		return fmt.Errorf("身份绑定状态已变化")
	}
	entry.DeliveryState = deliveryState
	entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.Bindings[lookupKey] = entry
	return saveIdentityBindingState(path, state)
}

func loadIdentityBindingState(path string) (identityBindingState, error) {
	state := identityBindingState{Version: identityBindingStateVersion, Bindings: map[string]identityBindingEntry{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("读取身份绑定状态失败")
	}
	if json.Unmarshal(data, &state) != nil || state.Version != identityBindingStateVersion || state.Bindings == nil {
		return identityBindingState{}, fmt.Errorf("身份绑定状态无效，拒绝继续")
	}
	return state, nil
}

func saveIdentityBindingState(path string, state identityBindingState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建身份绑定状态目录失败")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("编码身份绑定状态失败")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".identity-bindings-*.json")
	if err != nil {
		return fmt.Errorf("创建身份绑定临时状态失败")
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func resolveIdentityCandidate(ctx context.Context, runtime config.Config, client wecomRequester, name string) (verifiedIdentity, error) {
	response, err := client.Request(ctx, "list_employees", map[string]any{})
	if err != nil || apiError(response) != nil {
		return verifiedIdentity{}, fmt.Errorf("企业微信员工目录暂不可用")
	}
	container := any(response)
	if result, ok := response["result"].(map[string]any); ok {
		container = result
	}
	object, _ := container.(map[string]any)
	users, ok := object["userlist"].([]any)
	if !ok {
		users, ok = object["employees"].([]any)
	}
	if !ok {
		return verifiedIdentity{}, fmt.Errorf("企业微信员工目录响应无效")
	}
	matches := make([]verifiedIdentity, 0, 1)
	for _, rawUser := range users {
		user, ok := rawUser.(map[string]any)
		if !ok {
			return verifiedIdentity{}, fmt.Errorf("企业微信员工目录响应无效")
		}
		displayName, _ := user["name"].(string)
		userid, _ := user["userid"].(string)
		status, active := initializeInteger(user["status"])
		if strings.EqualFold(strings.TrimSpace(displayName), name) && validMessageRecipient(userid) && active && status == 1 {
			matches = append(matches, verifiedIdentity{UserID: userid, DisplayName: strings.TrimSpace(displayName)})
		}
	}
	if len(matches) != 1 {
		return verifiedIdentity{}, fmt.Errorf("姓名未唯一匹配一个启用的企业微信员工，请提供企业微信通讯录中的完整姓名")
	}
	schema, err := config.LoadSchema(runtime.SchemaMirrorPath)
	if err != nil {
		return verifiedIdentity{}, err
	}
	field, ok := schema.Roles["Z-S09"]["企业微信成员或责任人"]
	if !ok || field.Type != "FIELD_TYPE_USER" || field.ID == "" {
		return verifiedIdentity{}, fmt.Errorf("Z-S09 缺少企业微信成员绑定字段")
	}
	if !runtime.Allows("get_records") {
		return verifiedIdentity{}, fmt.Errorf("实例白名单未允许读取 Z-S09 主体")
	}
	target, err := wecom.ResolveTarget(ctx, client, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S09", runtime.Allows)
	if err != nil {
		return verifiedIdentity{}, err
	}
	recordResponse, err := client.Request(ctx, "get_records", map[string]any{
		"docid": target.DocumentID, "sheet_id": target.SheetID,
		"key_type": "CELL_VALUE_KEY_TYPE_FIELD_ID", "limit": 200,
		"field_ids": []string{field.ID},
	})
	if err != nil || apiError(recordResponse) != nil || recordSetMayBeIncomplete(recordResponse, 200) {
		return verifiedIdentity{}, fmt.Errorf("无法取得完整 Z-S09 主体快照")
	}
	subjectMatches := make([]string, 0, 1)
	for _, rawRecord := range recordsFrom(recordResponse) {
		record, _ := rawRecord.(map[string]any)
		recordID, _ := record["record_id"].(string)
		values, _ := record["values"].(map[string]any)
		if recordID != "" && identityCellContainsUserID(values[field.ID], matches[0].UserID) {
			subjectMatches = append(subjectMatches, recordID)
		}
	}
	if len(subjectMatches) != 1 {
		return verifiedIdentity{}, fmt.Errorf("企业微信员工未唯一登记到 Z-S09，拒绝建立业务主体绑定")
	}
	matches[0].SubjectRecordID = subjectMatches[0]
	return matches[0], nil
}

func identityCellContainsUserID(value any, userid string) bool {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(strings.TrimSpace(key)))
			if normalized == "userid" {
				if text, ok := child.(string); ok && text == userid {
					return true
				}
			}
			if identityCellContainsUserID(child, userid) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if identityCellContainsUserID(child, userid) {
				return true
			}
		}
	}
	return false
}

func verifiedIdentityFromContext(ctx context.Context) (verifiedIdentity, bool) {
	identity, ok := ctx.Value(verifiedIdentityContextKey{}).(verifiedIdentity)
	return identity, ok && identity.UserID != "" && identity.SubjectRecordID != ""
}

func businessActorUserID(ctx context.Context, runtime config.Config) string {
	if identity, ok := verifiedIdentityFromContext(ctx); ok {
		return identity.UserID
	}
	return runtime.WecomOperatorUserID
}

var actorReferenceFields = map[string]string{
	"Z-S01": "需求提出主体",
	"Z-S02": "决策主体",
	"Z-S04": "操作主体",
	"Z-S05": "调度主体",
	"Z-S06": "发起主体",
}

func withVerifiedActorReference(ctx context.Context, input applyInput) (applyInput, error) {
	identity, ok := verifiedIdentityFromContext(ctx)
	if !ok {
		return input, nil
	}
	fieldTitle := actorReferenceFields[input.TargetRole]
	if fieldTitle == "" {
		return input, nil
	}
	want := []any{map[string]any{"record_id": identity.SubjectRecordID}}
	for index := range input.Records {
		if len(input.Records[index].Values) == 0 {
			return input, fmt.Errorf("每条记录至少要有一个业务字段")
		}
		existing, provided := input.Records[index].Values[fieldTitle]
		if provided {
			encodedExisting, _ := json.Marshal(existing)
			encodedWant, _ := json.Marshal(want)
			if !hmac.Equal(encodedExisting, encodedWant) {
				return input, fmt.Errorf("字段 %s 必须匹配已验证的 Z-S09 业务主体", fieldTitle)
			}
			continue
		}
		if input.Operation == "add_records" {
			input.Records[index].Values[fieldTitle] = want
		}
	}
	return input, nil
}
