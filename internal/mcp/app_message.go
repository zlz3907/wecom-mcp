package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

const appMessageCapabilityGroup = "app_message"

type appMessageInput struct {
	RecipientUserID string `json:"recipient_userid"`
	Text            string `json:"text"`
	IdempotencyKey  string `json:"idempotency_key"`
}

func (s *Server) sendApplicationMessage(ctx context.Context, runtime config.Config, client wecomRequester, raw json.RawMessage) (any, error) {
	var input appMessageInput
	if err := strictDecode(raw, &input, "recipient_userid", "text", "idempotency_key"); err != nil {
		return nil, err
	}
	if !validMessageRecipient(input.RecipientUserID) {
		return nil, fmt.Errorf("recipient_userid 非法")
	}
	if strings.TrimSpace(input.Text) == "" || !utf8.ValidString(input.Text) || len([]byte(input.Text)) > 2048 {
		return nil, fmt.Errorf("text 必须是 1 至 2048 字节的 UTF-8 文本")
	}
	if len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 {
		return nil, fmt.Errorf("idempotency_key 无效")
	}
	if err := verifyBoundOperator(ctx, runtime, client, appMessageCapabilityGroup); err != nil {
		return nil, err
	}
	if !runtime.AllowsInGroup(appMessageCapabilityGroup, "send_app_message") {
		return nil, fmt.Errorf("app_message 专用 capability 未允许 send_app_message")
	}
	if _, err := verifyInitializeOperatorEmployee(ctx, client, input.RecipientUserID); err != nil {
		return nil, fmt.Errorf("recipient_userid 未在当前固定租户员工目录中唯一启用")
	}

	digestData, _ := json.Marshal(map[string]any{
		"operation":                "send_app_message",
		"recipient_userid":         input.RecipientUserID,
		"text":                     input.Text,
		"idempotency_key":          input.IdempotencyKey,
		"business_operator_userid": runtime.WecomOperatorUserID,
		"native_api_actor":         "application",
	})
	digestSum := sha256.Sum256(digestData)
	digest := hex.EncodeToString(digestSum[:])
	if err := s.reserveWithOperator(runtime.StatePath, input.IdempotencyKey, digest, runtime.WecomOperatorUserID); err != nil {
		return nil, err
	}

	response, err := client.Request(ctx, "send_app_message", map[string]any{
		"touser":                   input.RecipientUserID,
		"msgtype":                  "text",
		"text":                     map[string]string{"content": input.Text},
		"safe":                     0,
		"enable_duplicate_check":   1,
		"duplicate_check_interval": 1800,
	})
	if err != nil {
		return nil, fmt.Errorf("企业微信消息发送结果不确定，保留幂等状态: %w", err)
	}
	receipt, err := verifiedMessageReceipt(response)
	if err != nil {
		return nil, fmt.Errorf("企业微信消息发送未取得成功回执，保留幂等状态: %w", err)
	}
	if err := s.completeStateWithOperator(runtime.StatePath, input.IdempotencyKey, digest, runtime.WecomOperatorUserID); err != nil {
		return withOperatorAudit(map[string]any{
			"state":             "sent_idempotency_completion_pending",
			"idempotency_key":   input.IdempotencyKey,
			"request_digest":    digest,
			"receipt_verified":  true,
			"recipient_userid":  input.RecipientUserID,
			"message_id":        receipt["message_id"],
			"idempotency_error": err.Error(),
		}, runtime.WecomOperatorUserID), nil
	}
	return withOperatorAudit(map[string]any{
		"state":            "sent",
		"idempotency_key":  input.IdempotencyKey,
		"request_digest":   digest,
		"receipt_verified": true,
		"recipient_userid": input.RecipientUserID,
		"message_id":       receipt["message_id"],
	}, runtime.WecomOperatorUserID), nil
}

func validMessageRecipient(value string) bool {
	if value == "" || strings.EqualFold(value, "@all") || value != strings.TrimSpace(value) || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char == '_' || char == '-' || char == '.' || char == '@' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
			return false
		}
	}
	return true
}

func verifiedMessageReceipt(response map[string]any) (map[string]any, error) {
	result, ok := response["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("响应缺少 result")
	}
	code, ok := initializeInteger(result["errcode"])
	if !ok || code != 0 {
		message, _ := result["errmsg"].(string)
		return nil, fmt.Errorf("errcode %d: %s", code, message)
	}
	for _, key := range []string{"invaliduser", "unlicenseduser"} {
		if value, _ := result[key].(string); strings.TrimSpace(value) != "" {
			return nil, fmt.Errorf("回执包含无效接收人")
		}
	}
	messageID, _ := result["msgid"].(string)
	if strings.TrimSpace(messageID) == "" || len(messageID) > 256 {
		return nil, fmt.Errorf("响应缺少有效 msgid")
	}
	return map[string]any{"message_id": messageID}, nil
}
