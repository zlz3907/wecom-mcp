package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type appMediaMessageFake struct {
	employees   []any
	uploadType  string
	uploadName  string
	uploadBody  []byte
	sendPayload map[string]any
}

func (f *appMediaMessageFake) UploadAppMedia(_ context.Context, mediaType, filename string, content []byte) (map[string]any, error) {
	f.uploadType, f.uploadName, f.uploadBody = mediaType, filename, append([]byte(nil), content...)
	return map[string]any{"result": map[string]any{"errcode": float64(0), "errmsg": "ok", "type": mediaType, "media_id": "media-probe-1"}}, nil
}

func (f *appMediaMessageFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	switch operation {
	case "list_employees":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "userlist": f.employees}}, nil
	case "send_app_message":
		f.sendPayload, _ = payload.(map[string]any)
		return map[string]any{"result": map[string]any{"errcode": float64(0), "errmsg": "ok", "msgid": "message-media-1"}}, nil
	default:
		return nil, nil
	}
}

func TestSendApplicationMediaMessageUploadsAndSendsToOneEnabledUser(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	sum := sha256.Sum256(png)
	fake := &appMediaMessageFake{employees: []any{
		map[string]any{"userid": "operator", "status": float64(1)},
		map[string]any{"userid": "recipient", "status": float64(1)},
	}}
	runtime := config.Config{
		WecomOperatorUserID: "operator",
		StatePath:           filepath.Join(t.TempDir(), "state.json"),
		APIWhitelist: map[string][]string{
			appMessageCapabilityGroup: {"list_employees", "upload_app_media", "send_app_message"},
		},
	}
	input, _ := json.Marshal(map[string]string{
		"recipient_userid": "recipient",
		"media_type":       "image",
		"filename":         "probe.png",
		"content_base64":   base64.StdEncoding.EncodeToString(png),
		"content_sha256":   hex.EncodeToString(sum[:]),
		"idempotency_key":  "media-message-key-0001",
	})
	result, err := (&Server{}).sendApplicationMediaMessage(context.Background(), runtime, fake, input)
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "sent" || output["message_id"] != "message-media-1" || output["media_type"] != "image" {
		t.Fatalf("unexpected result: %#v", output)
	}
	if fake.uploadType != "image" || fake.uploadName != "probe.png" || len(fake.uploadBody) != len(png) {
		t.Fatalf("unexpected upload: type=%q name=%q size=%d", fake.uploadType, fake.uploadName, len(fake.uploadBody))
	}
	image, _ := fake.sendPayload["image"].(map[string]string)
	if fake.sendPayload["touser"] != "recipient" || fake.sendPayload["msgtype"] != "image" || image["media_id"] != "media-probe-1" || fake.sendPayload["toparty"] != nil || fake.sendPayload["totag"] != nil {
		t.Fatalf("unsafe or incomplete send payload: %#v", fake.sendPayload)
	}
}

func TestSendApplicationMediaMessageRejectsBroadcastAndHashMismatchBeforeUpload(t *testing.T) {
	fake := &appMediaMessageFake{}
	runtime := config.Config{StatePath: filepath.Join(t.TempDir(), "state.json"), APIWhitelist: map[string][]string{appMessageCapabilityGroup: {"list_employees", "upload_app_media", "send_app_message"}}}
	for _, recipient := range []string{"@all", "@ALL"} {
		input, _ := json.Marshal(map[string]string{"recipient_userid": recipient, "media_type": "file", "filename": "probe.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("content")), "content_sha256": hex.EncodeToString(make([]byte, 32)), "idempotency_key": "media-message-reject-" + recipient})
		if _, err := (&Server{}).sendApplicationMediaMessage(context.Background(), runtime, fake, input); err == nil {
			t.Fatalf("broadcast accepted: %s", recipient)
		}
	}
	if fake.uploadBody != nil || fake.sendPayload != nil {
		t.Fatal("rejected media reached upstream")
	}
}
