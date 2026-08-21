package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
)

func progressSchema() config.Schema {
	return config.Schema{Roles: map[string]map[string]config.Field{
		"Z-S01": {
			"计划任务基线": {Title: "计划任务基线", ID: "baseline", Type: "FIELD_TYPE_NUMBER"},
			"当前任务总数": {Title: "当前任务总数", ID: "current", Type: "FIELD_TYPE_NUMBER"},
			"已完成任务数": {Title: "已完成任务数", ID: "completed", Type: "FIELD_TYPE_NUMBER"},
			"阻塞任务数":  {Title: "阻塞任务数", ID: "blocked", Type: "FIELD_TYPE_NUMBER"},
		},
		"Z-S03": {
			"主需求":  {Title: "主需求", ID: "requirement", Type: "FIELD_TYPE_REFERENCE"},
			"任务状态": {Title: "任务状态", ID: "status", Type: "FIELD_TYPE_SINGLE_SELECT"},
		},
	}}
}

func TestNewRequirementProgressDefaultsToZero(t *testing.T) {
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"需求标题": "真实需求"}}}}
	got, err := withRequirementProgressDefaults(progressSchema(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range requirementProgressFields {
		if value, exists := got.Records[0].Values[title]; !exists || value != float64(0) {
			t.Fatalf("%s=%#v, want zero", title, value)
		}
	}
}

func TestNewRequirementRejectsNonZeroProgressValues(t *testing.T) {
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"当前任务总数": float64(3)}}}}
	if _, err := withRequirementProgressDefaults(progressSchema(), input); err == nil {
		t.Fatal("new requirement cannot start with a non-zero task count")
	}
}

func TestNewRequirementFailsClosedWhenProgressSchemaIsIncomplete(t *testing.T) {
	schema := progressSchema()
	delete(schema.Roles["Z-S01"], "阻塞任务数")
	input := applyInput{TargetRole: "Z-S01", Operation: "add_records", Records: []recordInput{{Values: map[string]any{"需求标题": "真实需求"}}}}
	if _, err := withRequirementProgressDefaults(schema, input); err == nil {
		t.Fatal("missing zero-value field must fail closed")
	}
}

func TestSummarizeRequirementProgressUsesLifecycleStates(t *testing.T) {
	records := []any{
		taskRow("t1", "req-1", "待执行"),
		taskRow("t2", "req-1", "已完成"),
		taskRow("t3", "req-1", "阻塞"),
		taskRow("t4", "req-1", "已取消"),
		taskRow("t5", "req-2", "已完成"),
	}
	got := summarizeRequirementProgress(records, "requirement", "status")
	if got["req-1"] != (requirementProgress{Current: 3, Completed: 1, Blocked: 1}) {
		t.Fatalf("req-1=%#v", got["req-1"])
	}
	if got["req-2"] != (requirementProgress{Current: 1, Completed: 1, Blocked: 0}) {
		t.Fatalf("req-2=%#v", got["req-2"])
	}
}

func TestCollectAffectedRequirementsIncludesOldAndNewRequirement(t *testing.T) {
	affected := map[string]struct{}{}
	collectAffectedRequirements(affected, []string{"task"}, taskResponse(taskRow("task", "old", "待执行")), "requirement")
	collectAffectedRequirements(affected, []string{"task"}, taskResponse(taskRow("task", "new", "待执行")), "requirement")
	if len(affected) != 2 {
		t.Fatalf("affected=%v", affected)
	}
	if _, ok := affected["old"]; !ok {
		t.Fatal("old requirement must be recalculated")
	}
	if _, ok := affected["new"]; !ok {
		t.Fatal("new requirement must be recalculated")
	}
}

func TestRequirementProgressUpdatesWritesOnlyDerivedCounters(t *testing.T) {
	updates, summary, err := requirementProgressUpdates(progressSchema(), map[string]struct{}{"req": {}}, map[string]requirementProgress{"req": {Current: 4, Completed: 2, Blocked: 1}})
	if err != nil {
		t.Fatal(err)
	}
	values := updates[0]["values"].(map[string]any)
	if len(values) != 3 || values["current"] != float64(4) || values["completed"] != float64(2) || values["blocked"] != float64(1) {
		t.Fatalf("values=%#v", values)
	}
	if _, exists := values["baseline"]; exists {
		t.Fatal("task lifecycle must not rewrite the approved planning baseline")
	}
	if len(summary) != 1 {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestRequirementProgressDetectsChangedSnapshot(t *testing.T) {
	affected := map[string]struct{}{"req": {}}
	before := map[string]requirementProgress{"req": {Current: 1}}
	after := map[string]requirementProgress{"req": {Current: 2}}
	if sameRequirementProgress(affected, before, after) {
		t.Fatal("changed task snapshot must trigger reconciliation")
	}
	if !sameRequirementProgress(affected, after, after) {
		t.Fatal("stable task snapshot must complete reconciliation")
	}
}

func TestRecordSetMayBeIncompleteFailsClosed(t *testing.T) {
	if !recordSetMayBeIncomplete(map[string]any{"result": map[string]any{"records": []any{}, "has_more": true}}, 200) {
		t.Fatal("has_more must be incomplete")
	}
	if !recordSetMayBeIncomplete(map[string]any{"result": map[string]any{"records": []any{}, "next_cursor": "cursor"}}, 200) {
		t.Fatal("next_cursor must be incomplete")
	}
	records := make([]any, 200)
	if !recordSetMayBeIncomplete(map[string]any{"result": map[string]any{"records": records}}, 200) {
		t.Fatal("a full page without pagination proof must fail closed")
	}
}

func TestProgressFileLockSerializesAcrossProcesses(t *testing.T) {
	statePath := t.TempDir() + "/state.json"
	release, err := acquireProgressFileLock(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := acquireProgressFileLock(ctx, statePath); err == nil {
		t.Fatal("second process lock must wait or time out")
	}
	release()
	releaseAgain, err := acquireProgressFileLock(context.Background(), statePath)
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain()
}

func taskRow(id, requirement, status string) map[string]any {
	return map[string]any{"record_id": id, "values": map[string]any{
		"requirement": []any{requirement},
		"status":      []any{map[string]any{"text": status}},
	}}
}

func taskResponse(rows ...map[string]any) map[string]any {
	records := make([]any, 0, len(rows))
	for _, row := range rows {
		records = append(records, row)
	}
	return map[string]any{"result": map[string]any{"records": records}}
}
