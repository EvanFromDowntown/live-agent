package webagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"liveagent/internal/domain"
	"liveagent/internal/sandbox"
)

// Execute applies one action. Bad or missing parameters are safe no-ops (never a
// crash) so the agent can recover on the next tick.
func (e *Env) Execute(ctx context.Context, action domain.Action) (domain.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.tick++
	e.step++
	e.lastAction = action.Name

	reward := domain.RewardVector{}
	info := map[string]any{}
	text := ""

	filesBefore := len(e.listFiles())

	switch action.Name {
	case "run_python", "run_shell":
		code, key := "", "code"
		if action.Name == "run_shell" {
			key = "script"
		}
		code, _ = action.Parameters[key].(string)
		if strings.TrimSpace(code) == "" {
			reward.RiskCost += 0.5
			text = "no code provided"
			break
		}
		e.budget--
		reward.ResourceCost = 1
		lang := "python"
		if action.Name == "run_shell" {
			lang = "shell"
		}
		// Release the lock during the (slow) container run so Observe/Tick from
		// other goroutines don't block; re-acquire afterwards.
		e.mu.Unlock()
		res, err := e.box.Run(ctx, sandbox.Spec{Lang: lang, Code: code})
		e.mu.Lock()
		if err != nil {
			e.lastStdout, e.lastStderr, e.lastExit = "", err.Error(), -1
			reward.RiskCost += 2
			text = "sandbox error: " + err.Error()
			break
		}
		e.lastStdout, e.lastStderr, e.lastExit, e.lastTimedOut = res.Stdout, res.Stderr, res.ExitCode, res.TimedOut
		if res.ExitCode == 0 {
			reward.InformationGain += 1
			if len(strings.TrimSpace(res.Stdout)) > 0 {
				reward.InformationGain += 1
			}
			text = fmt.Sprintf("ran %s ok in %dms; records=%d", lang, res.DurationMs, e.recordCount())
		} else {
			reward.RiskCost += 1
			text = fmt.Sprintf("%s exited %d: %s", lang, res.ExitCode, firstLine(res.Stderr))
		}

	case "write_file":
		path, _ := action.Parameters["path"].(string)
		content, _ := action.Parameters["content"].(string)
		if err := e.writeWorkspaceFile(path, content); err != nil {
			reward.RiskCost += 0.5
			text = "write failed: " + err.Error()
		} else {
			reward.InformationGain += 0.5
			text = "wrote " + path
		}

	case "read_file":
		path, _ := action.Parameters["path"].(string)
		content, err := e.readWorkspaceFile(path)
		if err != nil {
			reward.RiskCost += 0.2
			text = "read failed: " + err.Error()
		} else {
			e.lastStdout = content
			info["content"] = content
			text = "read " + path
		}

	case "finish":
		text = "agent declared finish"
		info["finished"] = true

	default:
		reward.RiskCost += 1
		text = "unknown action ignored"
	}

	// Reward newly created files as information gain.
	if newFiles := len(e.listFiles()) - filesBefore; newFiles > 0 {
		reward.InformationGain += float64(newFiles)
	}

	success := e.recordCount() >= e.successMin
	finished, _ := info["finished"].(bool)
	done := success || finished || e.step >= e.maxSteps || e.budget <= 0

	if success {
		reward.TaskSuccess += 5
	}
	// Homeostasis: reward keeping budget healthy.
	if e.budgetInit > 0 {
		reward.Homeostasis += e.budget/e.budgetInit - 0.5
	}

	info["episode_done"] = done
	info["episode_success"] = success
	info["death"] = false

	obs := e.observation()
	if done {
		e.resetEpisode()
	}

	return domain.Outcome{Success: success, Observation: obs, Reward: reward, Info: info, Text: text}, nil
}

func (e *Env) resetEpisode() {
	e.episodeStart = e.tick
	e.step = 0
	e.budget = e.budgetInit
	// Note: workspace files are intentionally preserved across episodes so the
	// agent can build on prior work (continual progress).
}

// ---- workspace helpers (path-confined to the sandbox workdir) --------------

func (e *Env) workspaceRoot() string { return e.box.WorkDir() }

// safePath resolves a user path and guarantees it stays inside the workspace.
func (e *Env) safePath(rel string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimSpace(rel)) // force absolute, strip ..
	full := filepath.Join(e.workspaceRoot(), clean)
	if !strings.HasPrefix(full, e.workspaceRoot()) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return full, nil
}

func (e *Env) writeWorkspaceFile(rel, content string) error {
	full, err := e.safePath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func (e *Env) readWorkspaceFile(rel string) (string, error) {
	full, err := e.safePath(rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if len(b) > 8192 {
		b = append(b[:8192], []byte("\n...[truncated]")...)
	}
	return string(b), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:] // the final line usually holds the exception message
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (e *Env) listFiles() []string {
	entries, err := os.ReadDir(e.workspaceRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, en := range entries {
		name := en.Name()
		// Hide bookkeeping entries: transient scripts (.run_*) and self-install
		// dirs (.pydeps, .ms-playwright, HOME caches) — all start with a dot.
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// recordCount returns how many SUBSTANTIVE records the success file contains.
// A record only counts if it carries real extracted content — this closes the
// loophole where the agent "succeeds" by writing empty/placeholder records
// (e.g. {"content":""} or {"content":"NO_ANSWERS_FOUND"}). For a JSON array,
// each element must satisfy substantive(); for a text file, each line must be a
// substantive value.
func (e *Env) recordCount() int {
	full, err := e.safePath(e.successFile)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return 0
	}
	var arr []any
	if json.Unmarshal(b, &arr) == nil {
		n := 0
		for _, rec := range arr {
			if e.substantive(rec) {
				n++
			}
		}
		return n
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if e.substantiveText(line) {
			n++
		}
	}
	return n
}

// placeholderTokens are obvious "no real data" markers the agent should not be
// able to pass off as extracted content.
var placeholderTokens = []string{"no_answers_found", "no answers found", "not found", "n/a", "none", "null", "placeholder", "todo", "unknown", "no data", "no_data"}

func isPlaceholder(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return true
	}
	for _, t := range placeholderTokens {
		if l == t {
			return true
		}
	}
	return false
}

func (e *Env) substantiveText(s string) bool {
	s = strings.TrimSpace(s)
	if isPlaceholder(s) {
		return false
	}
	return len([]rune(s)) >= e.minFieldChars
}

// substantive decides whether one record carries real content. For JSON objects
// with SuccessFields configured, every listed field must be a non-placeholder
// string of at least minFieldChars. Without SuccessFields, at least one string
// value must qualify. Non-object records fall back to a length/placeholder check.
func (e *Env) substantive(rec any) bool {
	m, ok := rec.(map[string]any)
	if !ok {
		return e.substantiveText(fmt.Sprintf("%v", rec))
	}
	fieldOK := func(v any) bool {
		s, ok := v.(string)
		if !ok {
			// Non-empty non-string values (e.g. a nested list of answers) count.
			return v != nil
		}
		s = strings.TrimSpace(s)
		return !isPlaceholder(s) && len([]rune(s)) >= e.minFieldChars
	}
	if len(e.successFields) > 0 {
		for _, f := range e.successFields {
			if !fieldOK(m[f]) {
				return false
			}
		}
		return true
	}
	for _, v := range m {
		if fieldOK(v) {
			return true
		}
	}
	return false
}
