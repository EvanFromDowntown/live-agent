package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/safety"
	"liveagent/internal/store"
	"liveagent/internal/tool"
)

// scriptLLM returns scripted responses in order, then defaults to a finish call.
type scriptLLM struct {
	calls  int
	script []domain.LLMResponse
}

func (s *scriptLLM) Generate(_ context.Context, _ domain.LLMRequest) (domain.LLMResponse, error) {
	if s.calls < len(s.script) {
		r := s.script[s.calls]
		s.calls++
		return r, nil
	}
	s.calls++
	return call("finish", `{"summary":"done","success":true}`), nil
}

func call(name, args string) domain.LLMResponse {
	return domain.LLMResponse{ToolCalls: []domain.ToolCall{{ID: "c", Name: name, Arguments: args}}}
}

func newTestAgent(t *testing.T, llm domain.LLM) *Agent {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ts := tool.NewToolset()
	(&tool.Builtins{Workdir: dir}).RegisterAll(ts)
	cfg := config.Default()
	cfg.Agent.Workspace = dir
	off := false
	cfg.Verify.Enabled = &off // deterministic loop tests: no verifier calls
	return New(Deps{LLM: llm, Tools: ts, Store: st, Guard: safety.NewGuard(cfg.Safety), Config: cfg, Workdir: dir})
}

func TestRunFinishes(t *testing.T) {
	llm := &scriptLLM{script: []domain.LLMResponse{
		call("update_plan", `{"plan":[{"step":"say hi","status":"in_progress"}]}`),
		call("run_shell", `{"command":"echo hello"}`),
		call("finish", `{"summary":"greeted","success":true}`),
	}}
	res, err := newTestAgent(t, llm).Run(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Finished || !res.Success {
		t.Fatalf("expected finished+success, got %+v", res)
	}
	if res.Steps != 3 {
		t.Fatalf("expected 3 steps, got %d", res.Steps)
	}
}

func TestRepeatDetection(t *testing.T) {
	// Always emit the exact same shell call → should stop with reason "repeat".
	same := call("run_shell", `{"command":"echo loop"}`)
	llm := &scriptLLM{script: []domain.LLMResponse{same, same, same, same, same}}
	res, _ := newTestAgent(t, llm).Run(context.Background(), "loop forever")
	if res.StopReason != "repeat" {
		t.Fatalf("expected stop reason repeat, got %q", res.StopReason)
	}
}

func TestRecoverToolCallFromText(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})

	// Transcript-echo pattern.
	tc, ok := ag.recoverToolCall(`TOOL_CALL run_shell {"command":"ls -la"}`)
	if !ok || tc.Name != "run_shell" || !strings.Contains(tc.Arguments, "ls -la") {
		t.Fatalf("echo pattern failed: %+v ok=%v", tc, ok)
	}

	// Generic {"name","arguments"} pattern.
	tc, ok = ag.recoverToolCall(`Sure: {"name":"read_file","arguments":{"path":"x.txt"}}`)
	if !ok || tc.Name != "read_file" || !strings.Contains(tc.Arguments, "x.txt") {
		t.Fatalf("generic pattern failed: %+v ok=%v", tc, ok)
	}

	// Unknown tool name → not recovered.
	if _, ok := ag.recoverToolCall(`TOOL_CALL not_a_tool {"x":1}`); ok {
		t.Fatal("should not recover unknown tool")
	}
	// Plain prose → not recovered.
	if _, ok := ag.recoverToolCall("Let me think about this problem."); ok {
		t.Fatal("should not recover from prose")
	}
}

func TestRecoveredCallExecutes(t *testing.T) {
	// Model never emits a native tool_call, only text — the loop must recover it.
	textCall := domain.LLMResponse{Text: `TOOL_CALL finish {"summary":"ok","success":true}`}
	res, _ := newTestAgent(t, &scriptLLM{script: []domain.LLMResponse{textCall}}).Run(context.Background(), "t")
	if !res.Finished || !res.Success {
		t.Fatalf("expected recovered finish, got %+v", res)
	}
}

func TestExecuteBatchParallelAndOrder(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	dir := ag.workdir
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("AAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("BBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	batch := []domain.ToolCall{
		{Name: "read_file", Arguments: `{"path":"a.txt"}`},
		{Name: "read_file", Arguments: `{"path":"b.txt"}`},
		{Name: "write_file", Arguments: `{"path":"c.txt","content":"CCC"}`},
	}
	res := ag.executeBatch(context.Background(), 1, batch)
	if len(res) != 3 {
		t.Fatalf("expected 3 results, got %d", len(res))
	}
	if res[0].res.Output != "AAA" || res[1].res.Output != "BBB" {
		t.Fatalf("read results not aligned/ordered: %q %q", res[0].res.Output, res[1].res.Output)
	}
	if res[2].res.IsError {
		t.Fatalf("write failed: %+v", res[2].res)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "c.txt")); string(data) != "CCC" {
		t.Fatalf("write_file did not run: %q", string(data))
	}
}

func TestExecuteBatchTerminalDedup(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	batch := []domain.ToolCall{
		{Name: "finish", Arguments: `{"summary":"first","success":true}`},
		{Name: "finish", Arguments: `{"summary":"second","success":true}`},
	}
	res := ag.executeBatch(context.Background(), 1, batch)
	if !res[0].res.Finished {
		t.Fatalf("first finish should execute: %+v", res[0].res)
	}
	if res[1].res.Finished || !res[1].res.IsError {
		t.Fatalf("second terminal call should be ignored as an error, got %+v", res[1].res)
	}
}

func TestExecuteBatchGuardBlocks(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ag.guard = safety.NewGuard(config.SafetyConfig{ForbiddenActions: []string{"run_shell"}})
	batch := []domain.ToolCall{
		{Name: "run_shell", Arguments: `{"command":"echo hi"}`},
		{Name: "read_file", Arguments: `{"path":"missing.txt"}`},
	}
	res := ag.executeBatch(context.Background(), 1, batch)
	if !res[0].blocked || !res[0].res.IsError {
		t.Fatalf("run_shell should be blocked: %+v", res[0])
	}
	if res[1].blocked {
		t.Fatalf("read_file should not be blocked")
	}
}

func TestParallelBatchRunFinishes(t *testing.T) {
	// One step emits two read-only calls together, then finish next step.
	multi := domain.LLMResponse{ToolCalls: []domain.ToolCall{
		{Name: "list_dir", Arguments: `{}`},
		{Name: "glob", Arguments: `{"pattern":"**/*"}`},
	}}
	llm := &scriptLLM{script: []domain.LLMResponse{
		multi,
		call("finish", `{"summary":"looked around","success":true}`),
	}}
	res, err := newTestAgent(t, llm).Run(context.Background(), "explore")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Finished || !res.Success {
		t.Fatalf("expected finished+success, got %+v", res)
	}
}

func TestNoToolCallStops(t *testing.T) {
	prose := domain.LLMResponse{Text: "I think we should..."}
	llm := &scriptLLM{script: []domain.LLMResponse{prose, prose, prose, prose, prose, prose, prose, prose}}
	res, _ := newTestAgent(t, llm).Run(context.Background(), "do nothing")
	if res.StopReason != "errors" {
		t.Fatalf("expected stop reason errors, got %q", res.StopReason)
	}
}
