package mcp

import (
	"errors"
	"testing"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func TestSchemaMutationOperationsCannotUseGenericAPI(t *testing.T) {
	for _, operation := range []string{"add_sheet", "update_sheet", "delete_sheet", "add_fields", "update_fields", "delete_fields"} {
		if !schemaMutationOperation(operation) {
			t.Fatalf("%s must be reserved for schema migrations", operation)
		}
	}
	for _, operation := range []string{"get_fields", "add_records", "update_records"} {
		if schemaMutationOperation(operation) {
			t.Fatalf("%s is not a schema mutation", operation)
		}
	}
}

func TestSchemaAdminRequiresConfiguredMatchingLocalIdentity(t *testing.T) {
	original := currentSchemaAdminUser
	t.Cleanup(func() { currentSchemaAdminUser = original })
	currentSchemaAdminUser = func() (string, error) { return "owner", nil }
	if err := verifySchemaAdmin(config.Config{SchemaAdminUser: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := verifySchemaAdmin(config.Config{SchemaAdminUser: "other"}); err == nil {
		t.Fatal("mismatched local identity must fail closed")
	}
	currentSchemaAdminUser = func() (string, error) { return "", errors.New("unavailable") }
	if err := verifySchemaAdmin(config.Config{SchemaAdminUser: "owner"}); err == nil {
		t.Fatal("unavailable local identity must fail closed")
	}
}

func TestLocalAdminIdentityMatchesWindowsDomainQualifiedAccount(t *testing.T) {
	for _, test := range []struct {
		name       string
		goos       string
		configured string
		current    string
		want       bool
	}{
		{name: "windows machine", goos: "windows", configured: `DESKTOP-123\zhyc`, current: `DESKTOP-123\zhyc`, want: true},
		{name: "windows case insensitive", goos: "windows", configured: `corp\zhyc`, current: `CORP\ZHYC`, want: true},
		{name: "windows bare", goos: "windows", configured: "zhyc", current: "zhyc", want: true},
		{name: "windows bare does not alias qualified", goos: "windows", configured: "zhyc", current: `CORP\zhyc`, want: false},
		{name: "windows different authority", goos: "windows", configured: `DESKTOP-123\zhyc`, current: `CORP\zhyc`, want: false},
		{name: "windows slash is not canonical", goos: "windows", configured: `CORP\zhyc`, current: `CORP/zhyc`, want: false},
		{name: "unix exact", goos: "darwin", configured: "zhyc", current: "zhyc", want: true},
		{name: "unix case sensitive", goos: "darwin", configured: "zhyc", current: "ZHYC", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := localAdminIdentityMatches(test.goos, test.configured, test.current); got != test.want {
				t.Fatalf("localAdminIdentityMatches(%q, %q, %q)=%v, want %v", test.goos, test.configured, test.current, got, test.want)
			}
		})
	}
}

func TestSubjectMigrationCatalogHasStableExpectedContract(t *testing.T) {
	fields := subjectMigrationFields("project-sheet", "project-field")
	if len(fields) != 16 || fields[0].Title != "主体编号" || fields[len(fields)-1].Title != "来源修订" {
		t.Fatalf("unexpected migration catalog: %#v", fields)
	}
	wire := migrationFieldWire(fields[6])
	property := wire["property_reference"].(map[string]any)
	if property["sub_id"] != "project-sheet" || property["field_id"] != "project-field" || property["is_multiple"] != true {
		t.Fatalf("unexpected project reference: %#v", property)
	}
	if labels := selectOptionLabels(fields[2].Property["property_single_select"]); len(labels) != 3 {
		t.Fatalf("subject type options=%#v", labels)
	}
}

func TestMigrationCompatibilityRejectsTypeOptionAndReferenceDrift(t *testing.T) {
	selectWanted := subjectMigrationFields("project-sheet", "project-field")[2]
	current := migrationFieldWire(selectWanted)
	current["field_id"] = "field"
	if err := compatibleMigrationField(current, selectWanted); err != nil {
		t.Fatal(err)
	}
	current["field_type"] = "FIELD_TYPE_TEXT"
	if err := compatibleMigrationField(current, selectWanted); err == nil {
		t.Fatal("type drift must fail")
	}

	referenceWanted := subjectMigrationFields("project-sheet", "project-field")[6]
	referenceCurrent := migrationFieldWire(referenceWanted)
	referenceCurrent["property_reference"] = map[string]any{"sub_id": "other", "field_id": "project-field", "is_multiple": true}
	if err := compatibleMigrationField(referenceCurrent, referenceWanted); err == nil {
		t.Fatal("reference drift must fail")
	}
}
