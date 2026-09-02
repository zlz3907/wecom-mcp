package wecom

import "testing"

func TestLegacyMCPEnterpriseWeComOperationMatrix(t *testing.T) {
	expected := map[string]Operation{
		"list_employees":       {"GET", "/cgi-bin/user/list?department_id=1&fetch_child=1", "read"},
		"get_doc_base_info":    {"POST", "/cgi-bin/wedoc/get_doc_base_info", "read"},
		"get_doc_share_url":    {"POST", "/cgi-bin/wedoc/doc_share", "read"},
		"get_doc_auth":         {"POST", "/cgi-bin/wedoc/doc_get_auth", "read"},
		"get_sheets":           {"POST", "/cgi-bin/wedoc/smartsheet/get_sheet", "read"},
		"get_views":            {"POST", "/cgi-bin/wedoc/smartsheet/get_views", "read"},
		"get_fields":           {"POST", "/cgi-bin/wedoc/smartsheet/get_fields", "read"},
		"get_records":          {"POST", "/cgi-bin/wedoc/smartsheet/get_records", "read"},
		"create_smartsheet":    {"POST", "/cgi-bin/wedoc/create_doc", "write"},
		"rename_document":      {"POST", "/cgi-bin/wedoc/rename_doc", "write"},
		"lock_down_doc_access": {"POST", "/cgi-bin/wedoc/mod_doc_join_rule", "write"},
		"grant_doc_readers":    {"POST", "/cgi-bin/wedoc/mod_doc_member", "write"},
		"harden_doc_security":  {"POST", "/cgi-bin/wedoc/mod_doc_safty_setting", "write"},
		"add_sheet":            {"POST", "/cgi-bin/wedoc/smartsheet/add_sheet", "write"},
		"update_sheet":         {"POST", "/cgi-bin/wedoc/smartsheet/update_sheet", "write"},
		"delete_sheet":         {"POST", "/cgi-bin/wedoc/smartsheet/delete_sheet", "write"},
		"add_view":             {"POST", "/cgi-bin/wedoc/smartsheet/add_view", "write"},
		"update_view":          {"POST", "/cgi-bin/wedoc/smartsheet/update_view", "write"},
		"delete_views":         {"POST", "/cgi-bin/wedoc/smartsheet/delete_views", "write"},
		"add_fields":           {"POST", "/cgi-bin/wedoc/smartsheet/add_fields", "write"},
		"update_fields":        {"POST", "/cgi-bin/wedoc/smartsheet/update_fields", "write"},
		"delete_fields":        {"POST", "/cgi-bin/wedoc/smartsheet/delete_fields", "write"},
		"add_records":          {"POST", "/cgi-bin/wedoc/smartsheet/add_records", "write"},
		"update_records":       {"POST", "/cgi-bin/wedoc/smartsheet/update_records", "write"},
		"delete_records":       {"POST", "/cgi-bin/wedoc/smartsheet/delete_records", "write"},
	}
	for name, want := range expected {
		got, ok := Operations[name]
		if !ok || got != want {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
	if len(expected) != 25 {
		t.Fatal("test inventory must list every legacy enterprise WeCom capability")
	}
	if got := Operations["get_sheet"]; got != expected["get_sheets"] {
		t.Fatalf("compatibility alias differs: %#v", got)
	}
	if got := Operations["send_app_message"]; got != (Operation{"POST", "/cgi-bin/message/send", "write"}) {
		t.Fatalf("managed application message operation differs: %#v", got)
	}
}
