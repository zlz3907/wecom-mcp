package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type identityBindingFake struct {
	messages []map[string]any
}

func (fake *identityBindingFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	if operation == "send_app_message" {
		message, _ := payload.(map[string]any)
		fake.messages = append(fake.messages, message)
		return map[string]any{"result": map[string]any{"errcode": float64(0), "errmsg": "ok", "msgid": "binding-message"}}, nil
	}
	return nil, nil
}

func identityBindingFixture(t *testing.T) (*Server, config.Config, *identityBindingFake) {
	t.Helper()
	t.Setenv(identityBindingSecretEnv, "0123456789abcdef0123456789abcdef")
	runtime := config.Config{
		InstanceName:               "identity-test",
		StatePath:                  filepath.Join(t.TempDir(), "state.json"),
		AIExecutionSubjectRecordID: "subject-ai",
		APIWhitelist: map[string][]string{
			appMessageCapabilityGroup: {"list_employees", "send_app_message"},
		},
	}
	server := &Server{identityCandidate: func(_ context.Context, _ config.Config, _ wecomRequester, name string) (verifiedIdentity, error) {
		if name == "第二个人" {
			return verifiedIdentity{UserID: "user-two", DisplayName: name, SubjectRecordID: "subject-two"}, nil
		}
		return verifiedIdentity{UserID: "user-one", DisplayName: name, SubjectRecordID: "subject-one"}, nil
	}}
	return server, runtime, &identityBindingFake{}
}

func verificationCodeFromMessage(t *testing.T, message map[string]any) string {
	t.Helper()
	text, _ := message["text"].(map[string]string)
	match := regexp.MustCompile(`[0-9]{6}`).FindString(text["content"])
	if match == "" || strings.Contains(text["content"], match+match) {
		t.Fatalf("verification code missing from protected message: %#v", message)
	}
	return match
}

func TestIdentityBindingHappyPathAndIdempotentStart(t *testing.T) {
	server, runtime, fake := identityBindingFixture(t)
	raw := json.RawMessage(`{"name":"第一人","idempotency_key":"identity-start-key-0001"}`)
	started, err := server.startIdentityBinding(context.Background(), runtime, fake, raw)
	if err != nil {
		t.Fatal(err)
	}
	result := started.(map[string]any)
	bindingID, _ := result["identity_binding_id"].(string)
	if result["state"] != "verification_code_sent" || len(bindingID) < 32 || len(fake.messages) != 1 || result["binding_expires"] != false {
		t.Fatalf("unexpected binding start: %#v messages=%d", result, len(fake.messages))
	}
	code := verificationCodeFromMessage(t, fake.messages[0])
	replayed, err := server.startIdentityBinding(context.Background(), runtime, fake, raw)
	if err != nil || replayed.(map[string]any)["idempotent_replay"] != true || len(fake.messages) != 1 {
		t.Fatalf("idempotent start resent message: result=%#v messages=%d err=%v", replayed, len(fake.messages), err)
	}
	confirmed, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": code}))
	if err != nil {
		t.Fatal(err)
	}
	bound := confirmed.(map[string]any)
	if bound["state"] != "bound" || bound["verified_userid"] != "user-one" || bound["zoop_subject_record_id"] != "subject-one" || bound["binding_expires"] != false {
		t.Fatalf("unexpected binding result: %#v", bound)
	}
	if bound["verified_execution_subject_record_id"] != "subject-ai" {
		t.Fatalf("configured AI execution subject missing: %#v", bound)
	}
	if _, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": code})); err == nil {
		t.Fatal("one-time verification code was accepted twice")
	}
}

func TestIdentityBindingRebindKeepsOldIdentityUntilConfirmation(t *testing.T) {
	server, runtime, fake := identityBindingFixture(t)
	started, err := server.startIdentityBinding(context.Background(), runtime, fake, json.RawMessage(`{"name":"第一人","idempotency_key":"identity-start-key-0002"}`))
	if err != nil {
		t.Fatal(err)
	}
	bindingID := started.(map[string]any)["identity_binding_id"].(string)
	firstCode := verificationCodeFromMessage(t, fake.messages[0])
	if _, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": firstCode})); err != nil {
		t.Fatal(err)
	}
	rebindInput := mustMarshal(map[string]string{"name": "第二个人", "idempotency_key": "identity-rebind-key-0001", "current_binding_id": bindingID})
	if _, err := server.startIdentityBinding(context.Background(), runtime, fake, rebindInput); err != nil {
		t.Fatal(err)
	}
	identity, entry, err := server.resolveIdentityBinding(runtime, bindingID)
	if err != nil || identity.UserID != "user-one" || entry.PendingUserID != "user-two" {
		t.Fatalf("old identity was not preserved during rebind: identity=%#v entry=%#v err=%v", identity, entry, err)
	}
	secondCode := verificationCodeFromMessage(t, fake.messages[1])
	confirmed, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": secondCode}))
	if err != nil || confirmed.(map[string]any)["verified_userid"] != "user-two" || confirmed.(map[string]any)["binding_generation"] != 2 {
		t.Fatalf("rebind failed: result=%#v err=%v", confirmed, err)
	}
}

func TestIdentityBindingLocksAfterFiveWrongCodes(t *testing.T) {
	server, runtime, fake := identityBindingFixture(t)
	started, err := server.startIdentityBinding(context.Background(), runtime, fake, json.RawMessage(`{"name":"第一人","idempotency_key":"identity-start-key-0003"}`))
	if err != nil {
		t.Fatal(err)
	}
	bindingID := started.(map[string]any)["identity_binding_id"].(string)
	code := verificationCodeFromMessage(t, fake.messages[0])
	wrongCode := "000000"
	if code == wrongCode {
		wrongCode = "000001"
	}
	for attempt := 1; attempt <= identityBindingMaxAttempts; attempt++ {
		if _, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": wrongCode})); err == nil {
			t.Fatalf("wrong code accepted on attempt %d", attempt)
		}
	}
	if _, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": code})); err == nil || !strings.Contains(err.Error(), "锁定") {
		t.Fatalf("locked code unexpectedly accepted: %v", err)
	}
}

func TestCompletedBindingCannotRestartWithoutExplicitRebind(t *testing.T) {
	server, runtime, fake := identityBindingFixture(t)
	raw := json.RawMessage(`{"name":"第一人","idempotency_key":"identity-start-key-0004"}`)
	started, err := server.startIdentityBinding(context.Background(), runtime, fake, raw)
	if err != nil {
		t.Fatal(err)
	}
	bindingID := started.(map[string]any)["identity_binding_id"].(string)
	code := verificationCodeFromMessage(t, fake.messages[0])
	if _, err := server.confirmIdentityBinding(runtime, mustMarshal(map[string]string{"identity_binding_id": bindingID, "verification_code": code})); err != nil {
		t.Fatal(err)
	}
	if _, err := server.startIdentityBinding(context.Background(), runtime, fake, raw); err == nil || !strings.Contains(err.Error(), "换绑") {
		t.Fatalf("completed binding restarted without explicit rebind: %v", err)
	}
}

func TestVerifiedActorReferenceIsInjectedAndCannotBeSpoofed(t *testing.T) {
	ctx := context.WithValue(context.Background(), verifiedIdentityContextKey{}, verifiedIdentity{UserID: "user-one", DisplayName: "第一人", SubjectRecordID: "subject-one"})
	ctx = context.WithValue(ctx, verifiedExecutionSubjectContextKey{}, verifiedExecutionSubject{RecordID: "subject-ai"})
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"需求标题": "测试"}}}}
	prepared, err := withVerifiedActorReferences(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	value := prepared.Records[0].Values["需求提出主体"]
	encoded, _ := json.Marshal(value)
	if string(encoded) != `[{"record_id":"subject-one"}]` {
		t.Fatalf("actor reference not injected: %s", encoded)
	}
	input.Records[0].Values["需求提出主体"] = []any{map[string]any{"record_id": "subject-other"}}
	if _, err := withVerifiedActorReferences(ctx, input); err == nil {
		t.Fatal("spoofed actor reference was accepted")
	}
	audit := applyInput{TargetRole: "Z-S04", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"审计事件编号": "AUDIT-1"}}}}
	prepared, err = withVerifiedActorReferences(ctx, audit)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(prepared.Records[0].Values["操作主体"])
	if string(encoded) != `[{"record_id":"subject-ai"}]` {
		t.Fatalf("AI executor reference not injected: %s", encoded)
	}
	session := applyInput{TargetRole: "Z-S06", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"会话编号与标题": "SESSION-1"}}}}
	prepared, err = withVerifiedActorReferences(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	initiator, _ := json.Marshal(prepared.Records[0].Values["发起主体"])
	executor, _ := json.Marshal(prepared.Records[0].Values["执行主体"])
	if string(initiator) != `[{"record_id":"subject-one"}]` || string(executor) != `[{"record_id":"subject-ai"}]` {
		t.Fatalf("session dual actors not injected: initiator=%s executor=%s", initiator, executor)
	}
	empty := applyInput{TargetRole: "Z-S02", Operation: "add_records", Records: []recordInput{{Values: map[string]any{}}}}
	if _, err := withVerifiedActorReferences(ctx, empty); err == nil {
		t.Fatal("actor injection turned an empty business record into a valid record")
	}
}

func TestConfiguredAIExecutionSubjectFailsClosed(t *testing.T) {
	if _, err := configuredAIExecutionSubject(config.Config{}); err == nil {
		t.Fatal("missing configured AI execution subject was accepted")
	}
	subject, err := configuredAIExecutionSubject(config.Config{AIExecutionSubjectRecordID: "subject-ai"})
	if err != nil || subject.RecordID != "subject-ai" {
		t.Fatalf("configured AI execution subject not resolved: %#v err=%v", subject, err)
	}
}

func TestIdentityCellContainsOnlyExplicitUserID(t *testing.T) {
	cell := []any{map[string]any{"type": "user", "user_id": "user-one", "name": "第一人"}}
	if !identityCellContainsUserID(cell, "user-one") || identityCellContainsUserID(cell, "第一人") || identityCellContainsUserID(map[string]any{"id": "user-one"}, "user-one") {
		t.Fatal("user cell matching widened beyond explicit userid")
	}
}

func TestResolveUniquePersonnelSubjectIgnoresAIWithSharedUserID(t *testing.T) {
	records := []any{
		identitySubjectRecord("human", "user-one", "人员主体", "启用"),
		identitySubjectRecord("workbuddy-ai", "user-one", "AI 执行主体", "启用"),
		identitySubjectRecord("codex-ai", "user-one", "AI 执行主体", "启用"),
	}
	recordID, err := resolveUniquePersonnelSubjectRecordID(records, "user-one", "member", "type", "status")
	if err != nil || recordID != "human" {
		t.Fatalf("shared AI userid blocked human binding: record=%q err=%v", recordID, err)
	}
}

func TestResolveUniquePersonnelSubjectRequiresOneEnabledHuman(t *testing.T) {
	tests := []struct {
		name    string
		records []any
	}{
		{name: "paused human", records: []any{identitySubjectRecord("human", "user-one", "人员主体", "暂停")}},
		{name: "AI only", records: []any{identitySubjectRecord("ai", "user-one", "AI 执行主体", "启用")}},
		{name: "duplicate enabled humans", records: []any{
			identitySubjectRecord("human-one", "user-one", "人员主体", "启用"),
			identitySubjectRecord("human-two", "user-one", "人员主体", "启用"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveUniquePersonnelSubjectRecordID(test.records, "user-one", "member", "type", "status"); err == nil {
				t.Fatal("invalid personnel subject set was accepted")
			}
		})
	}
}

func TestIdentityCellContainsOnlyExplicitText(t *testing.T) {
	cell := []any{map[string]any{"id": "option-one", "text": "人员主体"}}
	if !identityCellContainsText(cell, "人员主体") || identityCellContainsText(cell, "option-one") || identityCellContainsText(map[string]any{"name": "人员主体"}, "人员主体") {
		t.Fatal("select text matching widened beyond explicit text")
	}
}

func identitySubjectRecord(recordID, userid, subjectType, status string) map[string]any {
	return map[string]any{
		"record_id": recordID,
		"values": map[string]any{
			"member": []any{map[string]any{"id_type": float64(1), "user_id": userid}},
			"type":   []any{map[string]any{"text": subjectType}},
			"status": []any{map[string]any{"text": status}},
		},
	}
}
