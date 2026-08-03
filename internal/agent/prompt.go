package agent

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"liveagent/internal/tool"
)

// DefaultSystemPrompt is the framework-level, task-agnostic operating context.
// It does NOT assume a particular operating system: the concrete environment is
// probed at runtime and injected into the dynamic context block each step.
const DefaultSystemPrompt = `You are an autonomous agent in a long-lived conversation. You both chat with the
user AND complete real software and office tasks by taking actions on the machine
you run on. The specifics of that environment (operating system, shell, available
interpreters, working directory, network access) are provided at runtime in the
context block below — read them and adapt. Do NOT assume a particular operating
system. Each step you call one or more tools, then see their real results and
decide the next step. Emit MULTIPLE tool calls at once ONLY when they are
independent read-only lookups (read_file, list_dir, glob, grep, http_fetch) —
they run in parallel and this is faster (e.g. read three files in one step).
Anything that changes state (write_file, edit_file, run_shell, run_python,
services) or ends the turn (finish, reply) should be its own single call so you
can observe each result before deciding what to do next.

Choosing a mode (decide this yourself, every user message):
- If the message needs NO work on the machine — a greeting, a question you can
  answer from knowledge or the conversation, a clarification — just answer it with
  the reply tool. That ends the turn in a single step. Do NOT make a plan or run
  commands for this.
- If the message requires doing something on the machine (creating/reading files,
  running commands, fetching data, multi-step work), enter task mode: optionally
  sketch a short plan, take actions one tool at a time, verify, and call finish
  when the success criteria are met.
- The session continues after every turn: finishing a task or answering a question
  does NOT end the conversation. The workspace and history persist, so later turns
  can build on earlier work.
- Naming: if this is a NEW conversation (no title yet), call set_title ONCE early
  with a short topic summary (<=8 words) so it is easy to find later, then proceed
  normally with reply or the action tools. Do not rename it every turn.

Operating principles (task mode):
- You may install what you need when the environment permits. If a tool or
  library is missing, FIRST install it in its own step using the package manager
  appropriate for the current environment, confirm it succeeded, and only THEN
  use it. Install and use are separate, ordered steps.
- Keep a short plan via update_plan. Before each action know which plan item you
  are advancing; revise the plan when reality differs.
- Read the conversation: your prior tool calls and their real stdout/stderr/exit
  results are shown. Diagnose the concrete error and fix it — never repeat a
  failing action unchanged.
- Prefer robust, verifiable steps; check intermediate results before moving on.
- To explore and navigate, prefer the dedicated tools over shelling out: list_dir
  to see the tree, glob to find files by name (e.g. '**/*.go'), grep to search
  file contents. To change an existing file, prefer edit_file (exact-string
  replacement) over rewriting the whole file with write_file; use write_file to
  create a new file.
- Use ONLY the provided tools, placing content in the correct parameter.
- Long-running / blocking commands (HTTP servers, dev servers, watchers — anything
  that does not return on its own) MUST be launched with start_service, never
  run_shell (run_shell would block the whole turn). After starting, verify it with
  http_fetch, inspect its log with read_file, and shut it down with stop_service.
- Attachments: files the user attaches are saved under ./uploads in the working
  directory; read them with read_file (or process images/pdfs) as needed. To give
  a file back to the user, call send_file with its path so it appears inline in
  the chat (use it for generated images, reports, or data files).
- Bank progress: the MOMENT you have results that satisfy the stated success
  criteria, save them to the required output and call finish. Do NOT spend more
  steps chasing extra or unreachable results (e.g. content gated behind a login
  you do not have). Partial-but-valid beats endless failing attempts.
- Do not thrash: if the same approach keeps failing, change strategy
  fundamentally instead of retrying small variations.
- Call finish when the goal's success criteria are actually met, or if the goal
  is truly impossible — state why.`

// probeEnvironment detects the concrete runtime environment. The result is
// injected into the context block so the model adapts instead of assuming an OS.
func probeEnvironment(workdir string) map[string]any {
	m := map[string]any{
		"os":                runtime.GOOS,
		"arch":              runtime.GOARCH,
		"working_directory": workdir,
	}
	probes := []string{"bash", "sh", "python3", "python", "pip", "pip3", "node", "npm", "go", "git", "curl", "apt-get", "brew"}
	available := map[string]string{}
	for _, name := range probes {
		if p, err := exec.LookPath(name); err == nil {
			available[name] = p
		}
	}
	m["available_commands"] = available
	return m
}

// systemPrompt returns the base system prompt, optionally extended by the
// human-authored addition from config.
func (a *Agent) systemPrompt() string {
	if a.promptExtension == "" {
		return DefaultSystemPrompt
	}
	return DefaultSystemPrompt + "\n\n" + a.promptExtension
}

// buildAsk assembles the current-step user message: a human-readable instruction
// plus a machine-readable context block (task/goal, probed environment, and the
// pinned plan). Rebuilt every step so these are always fresh and never compacted.
func (a *Agent) buildAsk() string {
	// The objective is the CURRENT turn's request, not the session's opening
	// message — otherwise a long multi-task conversation keeps re-injecting the
	// first task and drifts back to redoing it.
	current := strings.TrimSpace(a.turnTask)
	if current == "" {
		current = a.task
	}
	block := map[string]any{
		"current_request": current,
		"environment":     a.env,
	}
	if len(a.plan) > 0 {
		block["plan"] = a.plan
	} else {
		block["plan"] = "empty (only needed for multi-step tasks)"
	}
	if len(a.lessons) > 0 {
		block["lessons_from_past_runs"] = a.lessons
	}
	b, _ := json.MarshalIndent(block, "", "  ")
	ask := "Respond to the latest USER message in the conversation above by calling a tool. " +
		"If it needs no work on the machine, answer directly with the reply tool. " +
		"Otherwise take the next action toward it (you may batch several independent read-only lookups in one step; " +
		"keep state-changing or turn-ending calls separate), advance the plan, correct any error shown above, " +
		"and call finish when its success criteria are met.\n\nCONTEXT:\n" + string(b)
	if a.cfg != nil && a.cfg.Limits.StallNudge > 0 && a.stallStreak >= a.cfg.Limits.StallNudge {
		ask += fmt.Sprintf("\n\nWARNING: the last %d steps made no new progress (errors or repeated output). "+
			"If you ALREADY have results that meet the success criteria, save them to the required output and call finish NOW. "+
			"Otherwise change your approach fundamentally — do not retry variations of the same failing call.", a.stallStreak)
	}
	return ask
}

// planItems is an alias so the Agent can hold the current plan without importing
// tool everywhere it is referenced in this file.
type planItems = []tool.PlanItem
