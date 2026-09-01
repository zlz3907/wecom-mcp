package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

const maxAppMediaBase64Chars = 28_000_000

type appMediaMessageInput struct {
	RecipientUserID string `json:"recipient_userid"`
	MediaType       string `json:"media_type"`
	Filename        string `json:"filename"`
	ContentBase64   string `json:"content_base64"`
	ContentSHA256   string `json:"content_sha256"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type appMediaRequester interface {
	wecomRequester
	UploadAppMedia(context.Context, string, string, []byte) (map[string]any, error)
}

func (s *Server) sendApplicationMediaMessage(ctx context.Context, runtime config.Config, client appMediaRequester, raw json.RawMessage) (any, error) {
	var input appMediaMessageInput
	if err := strictDecode(raw, &input, "recipient_userid", "media_type", "filename", "content_base64", "content_sha256", "idempotency_key"); err != nil {
		return nil, err
	}
	if !validMessageRecipient(input.RecipientUserID) {
		return nil, fmt.Errorf("recipient_userid 非法")
	}
	if input.MediaType != "image" && input.MediaType != "file" {
		return nil, fmt.Errorf("media_type 仅支持 image 或 file")
	}
	if len(input.IdempotencyKey) < 16 || len(input.IdempotencyKey) > 256 {
		return nil, fmt.Errorf("idempotency_key 无效")
	}
	if len(input.ContentBase64) == 0 || len(input.ContentBase64) > maxAppMediaBase64Chars || strings.TrimSpace(input.ContentBase64) != input.ContentBase64 {
		return nil, fmt.Errorf("content_base64 无效")
	}
	if len(input.ContentSHA256) != 64 || strings.ToLower(input.ContentSHA256) != input.ContentSHA256 {
		return nil, fmt.Errorf("content_sha256 必须是小写 SHA-256")
	}
	if _, err := hex.DecodeString(input.ContentSHA256); err != nil {
		return nil, fmt.Errorf("content_sha256 必须是小写 SHA-256")
	}
	if err := verifyBoundOperator(ctx, runtime, client, appMessageCapabilityGroup); err != nil {
		return nil, err
	}
	if !runtime.AllowsInGroup(appMessageCapabilityGroup, "upload_app_media") || !runtime.AllowsInGroup(appMessageCapabilityGroup, "send_app_message") {
		return nil, fmt.Errorf("app_message 专用 capability 未允许媒体上传与发送")
	}
	if _, err := verifyInitializeOperatorEmployee(ctx, client, input.RecipientUserID); err != nil {
		return nil, fmt.Errorf("recipient_userid 未在当前固定租户员工目录中唯一启用")
	}
	content, err := base64.StdEncoding.Strict().DecodeString(input.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("content_base64 不是规范 Base64")
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != input.ContentSHA256 {
		return nil, fmt.Errorf("content_sha256 与媒体内容不一致")
	}

	businessActor := businessActorUserID(ctx, runtime)
	digestData, _ := json.Marshal(map[string]any{
		"operation":                "send_app_media_message",
		"recipient_userid":         input.RecipientUserID,
		"media_type":               input.MediaType,
		"filename":                 input.Filename,
		"content_sha256":           input.ContentSHA256,
		"idempotency_key":          input.IdempotencyKey,
		"business_operator_userid": businessActor,
		"native_api_actor":         "application",
	})
	digestSum := sha256.Sum256(digestData)
	digest := hex.EncodeToString(digestSum[:])
	if err := s.reserveWithOperator(runtime.StatePath, input.IdempotencyKey, digest, businessActor); err != nil {
		return nil, err
	}

	uploadResponse, err := client.UploadAppMedia(ctx, input.MediaType, input.Filename, content)
	if err != nil {
		return nil, fmt.Errorf("企业微信媒体上传结果不确定，保留幂等状态: %w", err)
	}
	mediaID, err := verifiedMediaUploadReceipt(uploadResponse, input.MediaType)
	if err != nil {
		return nil, fmt.Errorf("企业微信媒体上传未取得成功回执，保留幂等状态: %w", err)
	}
	response, err := client.Request(ctx, "send_app_message", map[string]any{
		"touser":                   input.RecipientUserID,
		"msgtype":                  input.MediaType,
		input.MediaType:            map[string]string{"media_id": mediaID},
		"safe":                     0,
		"enable_duplicate_check":   1,
		"duplicate_check_interval": 1800,
	})
	if err != nil {
		return nil, fmt.Errorf("企业微信媒体消息发送结果不确定，保留幂等状态: %w", err)
	}
	receipt, err := verifiedMessageReceipt(response)
	if err != nil {
		return nil, fmt.Errorf("企业微信媒体消息发送未取得成功回执，保留幂等状态: %w", err)
	}
	if err := s.completeStateWithOperator(runtime.StatePath, input.IdempotencyKey, digest, businessActor); err != nil {
		return withOperatorAudit(map[string]any{
			"state":             "sent_idempotency_completion_pending",
			"idempotency_key":   input.IdempotencyKey,
			"request_digest":    digest,
			"receipt_verified":  true,
			"recipient_userid":  input.RecipientUserID,
			"media_type":        input.MediaType,
			"content_sha256":    input.ContentSHA256,
			"message_id":        receipt["message_id"],
			"idempotency_error": err.Error(),
		}, businessActor), nil
	}
	return withOperatorAudit(map[string]any{
		"state":            "sent",
		"idempotency_key":  input.IdempotencyKey,
		"request_digest":   digest,
		"receipt_verified": true,
		"recipient_userid": input.RecipientUserID,
		"media_type":       input.MediaType,
		"content_sha256":   input.ContentSHA256,
		"message_id":       receipt["message_id"],
	}, businessActor), nil
}

func verifiedMediaUploadReceipt(response map[string]any, expectedType string) (string, error) {
	result, ok := response["result"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("响应缺少 result")
	}
	code, ok := initializeInteger(result["errcode"])
	if !ok || code != 0 {
		message, _ := result["errmsg"].(string)
		return "", fmt.Errorf("errcode %d: %s", code, message)
	}
	mediaType, _ := result["type"].(string)
	if mediaType != expectedType {
		return "", fmt.Errorf("回执媒体类型不一致")
	}
	mediaID, _ := result["media_id"].(string)
	if strings.TrimSpace(mediaID) == "" || mediaID != strings.TrimSpace(mediaID) || len(mediaID) > 1024 {
		return "", fmt.Errorf("响应缺少有效 media_id")
	}
	return mediaID, nil
}
