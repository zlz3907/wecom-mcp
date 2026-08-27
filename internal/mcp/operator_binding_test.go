package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type operatorBindingFake struct {
	employees    []any
	auth         int
	grantCalls   int
	authReads    int
	grantErr     error
	grantVisible bool
	grantPayload map[string]any
}

func (f *operatorBindingFake) Request(_ context.Context, operation string, payload any) (map[string]any, error) {
	switch operation {
	case "list_employees":
		return map[string]any{"result": map[string]any{"errcode": float64(0), "userlist": f.employees}}, nil
	case "get_doc_auth":
		f.authReads++
		return map[string]any{"result": map[string]any{
			"errcode": float64(0), "access_rule": map[string]any{"internal": true}, "secure_setting": map[string]any{"copy": false},
			"doc_member_list": []any{map[string]any{"type": float64(1), "userid": "operator-byte-exact", "auth": float64(f.auth)}}, "co_auth_list": []any{},
		}}, nil
	case "grant_doc_readers":
		f.grantCalls++
		f.grantPayload, _ = payload.(map[string]any)
		if f.grantVisible {
			f.auth = 7
		}
		if f.grantErr != nil {
			return nil, f.grantErr
		}
		return map[string]any{"result": map[string]any{"errcode": float64(0)}}, nil
	default:
		return nil, fmt.Errorf("unexpected operation %s", operation)
	}
}

func activeOperator(userid string) any { return map[string]any{"userid": userid, "status": float64(1)} }

func TestOperatorDirectoryRequiresOneActiveByteExactEmployee(t *testing.T) {
	for _, test := range []struct {
		name      string
		employees []any
		wantOK    bool
	}{
		{"exact", []any{activeOperator("operator-byte-exact")}, true},
		{"case differs", []any{activeOperator("Operator-byte-exact")}, false},
		{"inactive", []any{map[string]any{"userid": "operator-byte-exact", "status": float64(2)}}, false},
		{"duplicate", []any{activeOperator("operator-byte-exact"), activeOperator("operator-byte-exact")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := verifyInitializeOperatorEmployee(context.Background(), &operatorBindingFake{employees: test.employees}, "operator-byte-exact")
			if (err == nil) != test.wantOK {
				t.Fatalf("directory result err=%v", err)
			}
		})
	}
}

func TestInitializerRepairsOperatorAdminAndReadsBackExactly(t *testing.T) {
	fake := &operatorBindingFake{auth: 1, grantVisible: true}
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "schema_staged", OperatorDigest: digestValue("operator-byte-exact"), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := ensureInitializeOperatorAdmin(context.Background(), fake, "document-id", "operator-byte-exact", "business", &journal, path); err != nil {
		t.Fatal(err)
	}
	if fake.grantCalls != 1 || fake.authReads != 2 || journal.PendingAdminOp != "" {
		t.Fatalf("repair evidence fake=%#v journal=%#v", fake, journal)
	}
	members, ok := fake.grantPayload["update_file_member_list"].([]any)
	if !ok || len(members) != 1 {
		t.Fatalf("unexpected grant payload: %#v", fake.grantPayload)
	}
	member := members[0].(map[string]any)
	if member["userid"] != "operator-byte-exact" || member["auth"] != 7 || member["type"] != 1 {
		t.Fatalf("operator was not granted auth=7 exactly: %#v", member)
	}
}

func TestInitializerUncertainAdminRepairNeverBlindlyRetries(t *testing.T) {
	fake := &operatorBindingFake{auth: 1, grantErr: fmt.Errorf("uncertain")}
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "schema_staged", OperatorDigest: digestValue("operator-byte-exact"), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := ensureInitializeOperatorAdmin(context.Background(), fake, "document-id", "operator-byte-exact", "registry", &journal, path); err == nil {
		t.Fatal("uncertain grant was accepted")
	}
	stored, _, exists, err := loadInstanceInitializeJournal(path)
	if err != nil || !exists || stored.PendingAdminOp == "" || stored.PendingAdminDocID != "document-id" {
		t.Fatalf("durable pending repair missing: %#v err=%v", stored, err)
	}
	if err := ensureInitializeOperatorAdmin(context.Background(), fake, "document-id", "operator-byte-exact", "registry", &stored, path); err == nil {
		t.Fatal("pending repair was retried")
	}
	if fake.grantCalls != 1 {
		t.Fatalf("uncertain repair repeated %d grants", fake.grantCalls)
	}
}

func TestInitializerAPIErrorCanResolveByExactAuthReadback(t *testing.T) {
	fake := &operatorBindingFake{auth: 1, grantErr: fmt.Errorf("transport lost"), grantVisible: true}
	path := filepath.Join(t.TempDir(), "journal.json")
	journal := instanceInitializeJournal{Version: instanceInitializeJournalV1, Phase: "schema_staged", OperatorDigest: digestValue("operator-byte-exact"), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := ensureInitializeOperatorAdmin(context.Background(), fake, "document-id", "operator-byte-exact", "business", &journal, path); err != nil {
		t.Fatal(err)
	}
	if fake.grantCalls != 1 || fake.authReads != 2 {
		t.Fatalf("uncertain response did not use one exact readback: %#v", fake)
	}
}
