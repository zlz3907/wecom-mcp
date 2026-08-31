package team

import (
	"encoding/json"
	"testing"
)

func TestClaimStringsSupportsNestedOIDCRoles(t *testing.T) {
	claims := map[string]json.RawMessage{
		"realm_access": json.RawMessage(`{"roles":["member","wecom-mcp-operator"]}`),
	}
	roles, err := claimStrings(claims, "realm_access.roles")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || roles[1] != "wecom-mcp-operator" {
		t.Fatalf("roles=%#v", roles)
	}
}

func TestResolveRoleUsesHighestAuthorizedRole(t *testing.T) {
	cfg := Config{ReaderRole: "reader-group", OperatorRole: "operator-group", AdminRole: "admin-group"}
	role, err := resolveRole([]string{"reader-group", "admin-group"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleAdmin {
		t.Fatalf("role=%q", role)
	}
	if _, err := resolveRole([]string{"unrelated"}, cfg); err == nil {
		t.Fatal("unrelated role must be denied")
	}
}

func TestConnectorAPIKeyAuthenticatorUsesReaderServiceIdentity(t *testing.T) {
	authenticator, err := NewConnectorAPIKeyAuthenticator(Config{AuthenticationMode: AuthenticationModeConnectorAPIKey, ConnectorAPIKey: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := authenticator.Verify(t.Context(), "0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := roleFromTokenInfo(info); !ok || role != RoleReader || info.UserID != "workbuddy-enterprise-connector" {
		t.Fatalf("unexpected connector identity: %#v", info)
	}
	if _, err := authenticator.Verify(t.Context(), "wrong", nil); err == nil {
		t.Fatal("wrong connector key must be rejected")
	}
}
