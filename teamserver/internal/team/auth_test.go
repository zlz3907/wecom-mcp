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
