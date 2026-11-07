package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

type schemaAdminFixture struct {
	employeeStatus int
	auth           int
	member         string
	doc            string
	malformed      bool
	duplicate      bool
	failure        string
	calls          int
}

func (f *schemaAdminFixture) Request(_ context.Context, op string, payload any) (map[string]any, error) {
	f.calls++
	if op == f.failure {
		return nil, errors.New("unavailable")
	}
	switch op {
	case "list_employees":
		return map[string]any{"result": map[string]any{"errcode": 0, "userlist": []any{map[string]any{"userid": f.member, "status": f.employeeStatus}}}}, nil
	case "get_doc_base_info":
		if payload.(map[string]any)["docid"] != f.doc {
			return nil, errors.New("wrong document")
		}
		return map[string]any{"result": map[string]any{"errcode": 0, "doc_type": 10, "doc_name": "Zoop"}}, nil
	case "get_doc_auth":
		if payload.(map[string]any)["docid"] != f.doc {
			return nil, errors.New("wrong document")
		}
		members := []any{map[string]any{"type": 1, "userid": f.member, "auth": f.auth}}
		if f.duplicate {
			members = append(members, members[0])
		}
		result := map[string]any{"errcode": 0, "access_rule": map[string]any{"internal": true}, "secure_setting": map[string]any{"copy": false}, "doc_member_list": members}
		if f.malformed {
			delete(result, "access_rule")
		}
		return map[string]any{"result": result}, nil
	}
	return nil, errors.New("unexpected operation")
}

func schemaAdminContext(user string, oauth bool) context.Context {
	ctx := context.WithValue(context.Background(), verifiedIdentityContextKey{}, verifiedIdentity{UserID: user, SubjectRecordID: "subject"})
	if oauth {
		ctx = context.WithValue(ctx, oauthInstanceDigestKey{}, "trusted-snapshot")
	}
	return ctx
}

func TestDiscoveredSchemaAdminRequiresExactLiveBusinessDocumentAdmin(t *testing.T) {
	runtime := config.Config{OAuthSchemaAdminUserID: "verified-member", WecomOperatorUserID: "verified-member", APIWhitelist: map[string][]string{"admin": {"list_employees", "get_doc_base_info", "get_doc_auth"}}}
	cases := []struct {
		name   string
		user   string
		oauth  bool
		mutate func(*schemaAdminFixture, *config.Config)
		doc    string
		want   bool
	}{
		{"verified document administrator", "verified-member", true, nil, "business-document", true},
		{"different OAuth member", "other-member", true, nil, "business-document", false},
		{"non OAuth transport", "verified-member", false, nil, "business-document", false},
		{"admin grant missing", "verified-member", true, func(_ *schemaAdminFixture, c *config.Config) { c.OAuthSchemaAdminUserID = "" }, "business-document", false},
		{"different runtime actor", "verified-member", true, func(_ *schemaAdminFixture, c *config.Config) { c.WecomOperatorUserID = "other" }, "business-document", false},
		{"inactive employee", "verified-member", true, func(f *schemaAdminFixture, _ *config.Config) { f.employeeStatus = 2 }, "business-document", false},
		{"business editor is not admin", "verified-member", true, func(f *schemaAdminFixture, _ *config.Config) { f.auth = 2 }, "business-document", false},
		{"duplicate document identity", "verified-member", true, func(f *schemaAdminFixture, _ *config.Config) { f.duplicate = true }, "business-document", false},
		{"incomplete authorization evidence", "verified-member", true, func(f *schemaAdminFixture, _ *config.Config) { f.malformed = true }, "business-document", false},
		{"permission endpoint unavailable", "verified-member", true, func(f *schemaAdminFixture, _ *config.Config) { f.failure = "get_doc_auth" }, "business-document", false},
		{"different business document", "verified-member", true, nil, "other-document", false},
		{"permission API not allowed", "verified-member", true, func(_ *schemaAdminFixture, c *config.Config) {
			c.APIWhitelist = map[string][]string{"read": {"list_employees"}}
		}, "business-document", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := runtime
			f := &schemaAdminFixture{employeeStatus: 1, auth: 7, member: "verified-member", doc: "business-document"}
			if tc.mutate != nil {
				tc.mutate(f, &c)
			}
			err := (&Server{discovered: true}).verifyDocumentSchemaAdmin(schemaAdminContext(tc.user, tc.oauth), c, f, tc.doc)
			if (err == nil) != tc.want {
				t.Fatalf("result err=%v want %v", err, tc.want)
			}
		})
	}
}
