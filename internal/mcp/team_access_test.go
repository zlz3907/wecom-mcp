package mcp

import "testing"

func TestEveryToolHasTeamAccessClassification(t *testing.T) {
	definitions, err := ToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != len(tools) {
		t.Fatalf("definitions=%d tools=%d", len(definitions), len(tools))
	}
	access := make(map[string]ToolAccess, len(definitions))
	for _, definition := range definitions {
		access[definition.Name] = definition.Access
	}
	for name, want := range map[string]ToolAccess{
		"wecom_record_query":           ToolAccessReader,
		"wecom_record_apply":           ToolAccessOperator,
		"wecom_registry_bootstrap":     ToolAccessAdmin,
		"wecom_schema_migration_apply": ToolAccessAdmin,
	} {
		if got := access[name]; got != want {
			t.Fatalf("%s access=%q, want %q", name, got, want)
		}
	}
}
