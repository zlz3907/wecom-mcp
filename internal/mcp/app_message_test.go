package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type appMessageFake struct {
	employees []any
	payload   map[string]any
	receipt   map[string]any
}

func (f *appMessageFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	switch operation {
	case "list_employees":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "userlist": f.employees}}, nil
	case "send_app_message":
		f.payload, _ = payload.(map[string]any)
		if f.receipt != nil {
			return f.receipt, nil
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0), "errmsg": "ok", "msgid": "message-receipt-1"}}, nil
	default:
		return nil, nil
	}
}

func TestSendApplicationMessageUsesManagedIdentityAndCompletesReceipt(t *testing.T) {
	fake := &appMessageFake{employees: []any{
		map[string]any{"userid": "operator", "status": float64(1)},
		map[string]any{"userid": "recipient", "status": float64(1)},
	}}
	runtime := config.Config{
		WecomOperatorUserID: "operator",
		StatePath:           filepath.Join(t.TempDir(), "state.json"),
		APIWhitelist: map[string][]string{
			appMessageCapabilityGroup: {"list_employees", "send_app_message"},
		},
	}
	server := &Server{}
	result, err := server.sendApplicationMessage(context.Background(), runtime, fake, json.RawMessage(`{"recipient_userid":"recipient","text":"hello","idempotency_key":"message-key-00001"}`))
	if err != nil {
		t.Fatal(err)
	}
	output := result.(map[string]any)
	if output["state"] != "sent" || output["message_id"] != "message-receipt-1" || output["receipt_verified"] != true {
		t.Fatalf("unexpected result: %#v", output)
	}
	if fake.payload["touser"] != "recipient" || fake.payload["msgtype"] != "text" || fake.payload["agentid"] != nil || fake.payload["enable_duplicate_check"] != 1 {
		t.Fatalf("unsafe or incomplete upstream payload: %#v", fake.payload)
	}
	state, err := loadState(runtime.StatePath)
	if err != nil || state.Entries["message-key-00001"].Status != "completed" {
		t.Fatalf("message idempotency state not completed: %#v err=%v", state, err)
	}
}

func TestSendApplicationMessageRejectsInactiveRecipientBeforeReservation(t *testing.T) {
	fake := &appMessageFake{employees: []any{
		map[string]any{"userid": "operator", "status": float64(1)},
		map[string]any{"userid": "recipient", "status": float64(2)},
	}}
	runtime := config.Config{
		WecomOperatorUserID: "operator",
		StatePath:           filepath.Join(t.TempDir(), "state.json"),
		APIWhitelist: map[string][]string{
			appMessageCapabilityGroup: {"list_employees", "send_app_message"},
		},
	}
	server := &Server{}
	_, err := server.sendApplicationMessage(context.Background(), runtime, fake, json.RawMessage(`{"recipient_userid":"recipient","text":"hello","idempotency_key":"message-key-00002"}`))
	if err == nil || fake.payload != nil {
		t.Fatalf("inactive recipient reached message send: payload=%#v err=%v", fake.payload, err)
	}
}

func TestSendApplicationMessageRejectsBroadcastRecipient(t *testing.T) {
	fake := &appMessageFake{}
	runtime := config.Config{
		WecomOperatorUserID: "operator",
		StatePath:           filepath.Join(t.TempDir(), "state.json"),
		APIWhitelist: map[string][]string{
			appMessageCapabilityGroup: {"list_employees", "send_app_message"},
		},
	}
	server := &Server{}
	for _, recipient := range []string{"@all", "@ALL"} {
		input, err := json.Marshal(map[string]string{
			"recipient_userid": recipient,
			"text":             "hello",
			"idempotency_key":  "message-broadcast-rejected-" + recipient,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := server.sendApplicationMessage(context.Background(), runtime, fake, input); err == nil {
			t.Fatalf("broadcast recipient accepted: %q", recipient)
		}
	}
	if fake.payload != nil {
		t.Fatalf("broadcast recipient reached message send: %#v", fake.payload)
	}
}

func TestVerifiedMessageReceiptRejectsPartialOrMissingReceipt(t *testing.T) {
	for _, response := range []map[string]any{
		{"result": map[string]any{"errcode": float64(0), "msgid": ""}},
		{"result": map[string]any{"errcode": float64(0), "msgid": "message", "invaliduser": "recipient"}},
		{"result": map[string]any{"errcode": float64(81013), "errmsg": "user not in visible scope"}},
	} {
		if _, err := verifiedMessageReceipt(response); err == nil {
			t.Fatalf("invalid receipt accepted: %#v", response)
		}
	}
}
