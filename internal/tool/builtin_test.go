package tool

import (
	"context"
	"testing"
)

func TestUpdatePlanAcceptsArrayAndString(t *testing.T) {
	up := &updatePlanTool{}

	// Native array form.
	arr := map[string]any{"plan": []any{
		map[string]any{"step": "a", "status": "in_progress"},
		map[string]any{"step": "b", "status": "pending"},
	}}
	if res := up.Execute(context.Background(), arr); res.IsError || !res.HasPlan || len(res.Plan) != 2 {
		t.Fatalf("array form failed: %+v", res)
	}

	// Stringified form (what GLM actually sends).
	str := map[string]any{"plan": `[{"step":"a","status":"in_progress"},{"step":"b","status":"pending"}]`}
	res := up.Execute(context.Background(), str)
	if res.IsError || !res.HasPlan || len(res.Plan) != 2 {
		t.Fatalf("string form failed: %+v", res)
	}
	if res.Plan[0].Step != "a" || res.Plan[0].Status != "in_progress" {
		t.Fatalf("unexpected parsed plan: %+v", res.Plan)
	}
}

func TestToolsetDefsAndValidate(t *testing.T) {
	ts := NewToolset()
	(&Builtins{Workdir: t.TempDir()}).RegisterAll(ts)
	defs := ts.Defs()
	if len(defs) == 0 {
		t.Fatal("expected tool defs")
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"run_shell", "run_python", "read_file", "write_file", "http_fetch", "update_plan", "finish"} {
		if !names[want] {
			t.Fatalf("missing tool %q", want)
		}
	}
}
