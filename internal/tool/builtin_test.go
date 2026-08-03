package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	for _, want := range []string{"run_shell", "run_python", "read_file", "write_file", "edit_file", "list_dir", "glob", "grep", "http_fetch", "start_service", "list_services", "stop_service", "send_file", "update_plan", "finish"} {
		if !names[want] {
			t.Fatalf("missing tool %q", want)
		}
	}
}

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true}, // **/ matches zero dirs
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "lib/a/b.ts", false},
		{"foo?.txt", "foo1.txt", true},
		{"foo?.txt", "foo12.txt", false},
	}
	for _, c := range cases {
		re, err := globToRegexp(c.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", c.pattern, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("glob %q vs %q = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	b := &Builtins{Workdir: dir}
	tool := &editFileTool{b}
	p := filepath.Join(dir, "x.txt")
	os.WriteFile(p, []byte("alpha beta alpha"), 0o644)

	// Non-unique without replace_all -> error.
	if res := tool.Execute(context.Background(), map[string]any{"path": "x.txt", "old_string": "alpha", "new_string": "Z"}); !res.IsError {
		t.Fatalf("expected non-unique error, got %+v", res)
	}
	// replace_all replaces both.
	if res := tool.Execute(context.Background(), map[string]any{"path": "x.txt", "old_string": "alpha", "new_string": "Z", "replace_all": true}); res.IsError {
		t.Fatalf("replace_all failed: %+v", res)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "Z beta Z" {
		t.Fatalf("got %q", string(data))
	}
	// Missing old_string -> error.
	if res := tool.Execute(context.Background(), map[string]any{"path": "x.txt", "old_string": "nope", "new_string": "Q"}); !res.IsError {
		t.Fatalf("expected not-found error, got %+v", res)
	}
}

func TestGlobAndGrepTools(t *testing.T) {
	dir := t.TempDir()
	b := &Builtins{Workdir: dir}
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc Hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("package sub\n// hello world\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "c.go"), []byte("skip me Hello\n"), 0o644)

	g := (&globTool{b}).Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if g.IsError || !strings.Contains(g.Output, "a.go") || !strings.Contains(g.Output, "sub/b.go") {
		t.Fatalf("glob output: %+v", g)
	}
	if strings.Contains(g.Output, "node_modules") {
		t.Fatalf("glob should skip node_modules: %s", g.Output)
	}

	gr := (&grepTool{b}).Execute(context.Background(), map[string]any{"pattern": "[Hh]ello", "include": "*.go"})
	if gr.IsError || !strings.Contains(gr.Output, "a.go:2") {
		t.Fatalf("grep output: %+v", gr)
	}
	if strings.Contains(gr.Output, "node_modules") {
		t.Fatalf("grep should skip node_modules: %s", gr.Output)
	}
}

func TestFileJail(t *testing.T) {
	dir := t.TempDir()
	b := &Builtins{Workdir: dir}
	// Reading a path that escapes the workspace must be blocked.
	esc := (&readFileTool{b}).Execute(context.Background(), map[string]any{"path": "../../../etc/hosts"})
	if !esc.IsError || !strings.Contains(esc.Output, "outside the workspace") {
		t.Fatalf("expected jail to block escape, got %+v", esc)
	}
	// Absolute path outside the workspace must be blocked too.
	absEsc := (&readFileTool{b}).Execute(context.Background(), map[string]any{"path": "/etc/hosts"})
	if !absEsc.IsError {
		t.Fatalf("expected jail to block absolute escape, got %+v", absEsc)
	}
	// A normal in-workspace write/read round-trips.
	if w := (&writeFileTool{b}).Execute(context.Background(), map[string]any{"path": "note.txt", "content": "hi"}); w.IsError {
		t.Fatalf("in-jail write failed: %+v", w)
	}
	if r := (&readFileTool{b}).Execute(context.Background(), map[string]any{"path": "note.txt"}); r.IsError || r.Output != "hi" {
		t.Fatalf("in-jail read failed: %+v", r)
	}
}

func TestRunShellStreamsToSink(t *testing.T) {
	b := &Builtins{Workdir: t.TempDir(), Timeout: 10 * time.Second}
	var got strings.Builder
	ctx := WithOutputSink(context.Background(), func(chunk string) { got.WriteString(chunk) })
	res := (&shellTool{b}).Execute(ctx, map[string]any{"command": "printf 'line1\\nline2\\n'"})
	if res.IsError {
		t.Fatalf("shell failed: %+v", res)
	}
	if !strings.Contains(got.String(), "line1") || !strings.Contains(got.String(), "line2") {
		t.Fatalf("sink did not receive streamed output: %q", got.String())
	}
	if !strings.Contains(res.Output, "line1") {
		t.Fatalf("final output missing content: %q", res.Output)
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	b := &Builtins{Workdir: dir}
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "deep.txt"), []byte("yo"), 0o644)

	shallow := (&listDirTool{b}).Execute(context.Background(), map[string]any{})
	if shallow.IsError || !strings.Contains(shallow.Output, "top.txt") || !strings.Contains(shallow.Output, "sub/") {
		t.Fatalf("list depth1: %+v", shallow)
	}
	if strings.Contains(shallow.Output, "deep.txt") {
		t.Fatalf("depth1 should not recurse: %s", shallow.Output)
	}
	deep := (&listDirTool{b}).Execute(context.Background(), map[string]any{"depth": 3})
	if !strings.Contains(deep.Output, "sub/deep.txt") {
		t.Fatalf("depth3 should include deep.txt: %s", deep.Output)
	}
}
