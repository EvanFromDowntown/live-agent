package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"liveagent/internal/domain"
)

// maxOutput bounds tool output fed back to the model so a single noisy command
// cannot blow the context budget. The middle is elided.
const maxOutput = 8000

// Builtins constructs the standard host tools. Execution is DIRECT on the host
// process (no sandbox) inside Workdir, per the chosen design.
type Builtins struct {
	Workdir   string
	Timeout   time.Duration
	PythonBin string // resolved python interpreter ("python3"/"python")
	http      *http.Client
}

// RegisterAll registers every built-in tool into the toolset.
func (b *Builtins) RegisterAll(ts *Toolset) {
	if b.Timeout <= 0 {
		b.Timeout = 120 * time.Second
	}
	if b.PythonBin == "" {
		b.PythonBin = resolvePython()
	}
	b.http = &http.Client{Timeout: b.Timeout}
	ts.Register(&shellTool{b})
	ts.Register(&pythonTool{b})
	ts.Register(&readFileTool{b})
	ts.Register(&writeFileTool{b})
	ts.Register(&httpFetchTool{b})
	ts.Register(&updatePlanTool{})
	ts.Register(&finishTool{})
}

func resolvePython() string {
	for _, p := range []string{"python3", "python"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return "python3"
}

// runCmd executes a command in Workdir with a timeout, returning combined
// stdout+stderr and whether it failed.
func (b *Builtins) runCmd(ctx context.Context, name string, args ...string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = b.Workdir
	out, err := cmd.CombinedOutput()
	s := clip(string(out))
	if cctx.Err() == context.DeadlineExceeded {
		return s + fmt.Sprintf("\n[timed out after %s]", b.Timeout), true
	}
	if err != nil {
		return fmt.Sprintf("%s\n[exit: %v]", s, err), true
	}
	if strings.TrimSpace(s) == "" {
		s = "[no output; exit 0]"
	}
	return s, false
}

func clip(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	head := s[:maxOutput*2/3]
	tail := s[len(s)-maxOutput/3:]
	return head + fmt.Sprintf("\n...[%d bytes elided]...\n", len(s)-len(head)-len(tail)) + tail
}

// resolve turns a possibly-relative path into one anchored under Workdir.
func (b *Builtins) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(b.Workdir, p)
}

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// -----------------------------------------------------------------------------
// run_shell
// -----------------------------------------------------------------------------

type shellTool struct{ b *Builtins }

func (t *shellTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "run_shell",
		Description: "Run a shell command (via sh -c) in the working directory. Returns combined stdout+stderr and exit status. Use this to install tools, inspect files, run programs.",
		Parameters: map[string]domain.ParamSpec{
			"command": {Type: "string", Required: true, Description: "The shell command to execute."},
		},
	}
}

func (t *shellTool) Execute(ctx context.Context, args map[string]any) Result {
	cmd := str(args, "command")
	if strings.TrimSpace(cmd) == "" {
		return Result{IsError: true, Output: "command is empty"}
	}
	out, failed := t.b.runCmd(ctx, "sh", "-c", cmd)
	return Result{Output: out, IsError: failed}
}

// -----------------------------------------------------------------------------
// run_python
// -----------------------------------------------------------------------------

type pythonTool struct{ b *Builtins }

func (t *pythonTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "run_python",
		Description: "Run a Python 3 script in the working directory. The code is written to a file and executed; returns combined stdout+stderr. Install third-party packages FIRST with run_shell (pip install ...) before importing them.",
		Parameters: map[string]domain.ParamSpec{
			"code": {Type: "string", Required: true, Description: "The Python source to execute."},
		},
	}
}

func (t *pythonTool) Execute(ctx context.Context, args map[string]any) Result {
	code := str(args, "code")
	if strings.TrimSpace(code) == "" {
		return Result{IsError: true, Output: "code is empty"}
	}
	f := filepath.Join(t.b.Workdir, ".agent_step.py")
	if err := os.WriteFile(f, []byte(code), 0o644); err != nil {
		return Result{IsError: true, Output: "cannot write script: " + err.Error()}
	}
	out, failed := t.b.runCmd(ctx, t.b.PythonBin, f)
	return Result{Output: out, IsError: failed}
}

// -----------------------------------------------------------------------------
// read_file / write_file
// -----------------------------------------------------------------------------

type readFileTool struct{ b *Builtins }

func (t *readFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "read_file",
		Description: "Read a UTF-8 text file (relative paths resolve against the working directory).",
		Parameters: map[string]domain.ParamSpec{
			"path": {Type: "string", Required: true, Description: "File path to read."},
		},
	}
}

func (t *readFileTool) Execute(_ context.Context, args map[string]any) Result {
	data, err := os.ReadFile(t.b.resolve(str(args, "path")))
	if err != nil {
		return Result{IsError: true, Output: "read failed: " + err.Error()}
	}
	return Result{Output: clip(string(data))}
}

type writeFileTool struct{ b *Builtins }

func (t *writeFileTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "write_file",
		Description: "Write (create/overwrite) a UTF-8 text file (relative paths resolve against the working directory).",
		Parameters: map[string]domain.ParamSpec{
			"path":    {Type: "string", Required: true, Description: "File path to write."},
			"content": {Type: "string", Required: true, Description: "File contents."},
		},
	}
}

func (t *writeFileTool) Execute(_ context.Context, args map[string]any) Result {
	p := t.b.resolve(str(args, "path"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{IsError: true, Output: "mkdir failed: " + err.Error()}
	}
	if err := os.WriteFile(p, []byte(str(args, "content")), 0o644); err != nil {
		return Result{IsError: true, Output: "write failed: " + err.Error()}
	}
	return Result{Output: fmt.Sprintf("wrote %d bytes to %s", len(str(args, "content")), p)}
}

// -----------------------------------------------------------------------------
// http_fetch
// -----------------------------------------------------------------------------

type httpFetchTool struct{ b *Builtins }

func (t *httpFetchTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "http_fetch",
		Description: "HTTP GET a URL and return the status code and response body (truncated). For anything more complex (headers, POST, cookies, JS) write code with run_python instead.",
		Parameters: map[string]domain.ParamSpec{
			"url": {Type: "string", Required: true, Description: "The URL to GET."},
		},
	}
}

func (t *httpFetchTool) Execute(ctx context.Context, args map[string]any) Result {
	url := str(args, "url")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{IsError: true, Output: "bad request: " + err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; live-agent/2)")
	resp, err := t.b.http.Do(req)
	if err != nil {
		return Result{IsError: true, Output: "fetch failed: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxOutput*2))
	out := fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, clip(string(body)))
	return Result{Output: out, IsError: resp.StatusCode >= 400}
}

// -----------------------------------------------------------------------------
// update_plan
// -----------------------------------------------------------------------------

type updatePlanTool struct{}

func (t *updatePlanTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "update_plan",
		Description: "Set or update your short TODO plan. Call this at the start and whenever the plan changes. Pass 'plan' as a JSON array of {\"step\": string, \"status\": \"pending\"|\"in_progress\"|\"done\"}.",
		Parameters: map[string]domain.ParamSpec{
			"plan": {Type: "any", Required: true, Description: "Array of {step, status} objects."},
		},
	}
}

func (t *updatePlanTool) Execute(_ context.Context, args map[string]any) Result {
	raw, err := coercePlanArray(args["plan"])
	if err != nil {
		return Result{IsError: true, Output: err.Error()}
	}
	var items []PlanItem
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		step, _ := m["step"].(string)
		status, _ := m["status"].(string)
		if step == "" {
			continue
		}
		if status == "" {
			status = "pending"
		}
		items = append(items, PlanItem{Step: step, Status: status})
	}
	if len(items) == 0 {
		return Result{IsError: true, Output: "no valid plan items"}
	}
	return Result{HasPlan: true, Plan: items, Output: fmt.Sprintf("plan updated (%d items)", len(items))}
}

// coercePlanArray accepts the plan either as a native JSON array (map decoding)
// or as a JSON-encoded string (many models stringify complex tool arguments).
func coercePlanArray(v any) ([]any, error) {
	switch p := v.(type) {
	case []any:
		return p, nil
	case string:
		var arr []any
		if err := json.Unmarshal([]byte(strings.TrimSpace(p)), &arr); err != nil {
			return nil, fmt.Errorf("plan string is not a JSON array: %v", err)
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("plan must be an array of {step, status} objects")
	}
}

// -----------------------------------------------------------------------------
// finish
// -----------------------------------------------------------------------------

type finishTool struct{}

func (t *finishTool) Spec() domain.ActionSchema {
	return domain.ActionSchema{
		Name:        "finish",
		Description: "End the run. Call this when the goal's success criteria are actually met, or when the goal is truly impossible. Provide a summary of what was accomplished (or why it failed).",
		Parameters: map[string]domain.ParamSpec{
			"summary": {Type: "string", Required: true, Description: "What was accomplished, or why the task cannot be completed."},
			"success": {Type: "bool", Required: false, Description: "true if the goal was achieved."},
		},
	}
}

func (t *finishTool) Execute(_ context.Context, args map[string]any) Result {
	success, _ := args["success"].(bool)
	return Result{Finished: true, Success: success, Output: str(args, "summary")}
}
