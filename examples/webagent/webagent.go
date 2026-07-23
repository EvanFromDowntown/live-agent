// Package webagent is an Environment whose actions are REAL code execution in a
// sandboxed container (see internal/sandbox). The agent pursues a task (e.g.
// "crawl this site and save the results") by writing and running Python/shell,
// reading its own output, and iterating — genuine tool use, not a toy grid.
//
// The LLM authors the code; the sandbox contains it. Only registered actions
// (run_python/run_shell/read_file/write_file/finish) can execute, and the
// container isolates the host filesystem while allowing network access.
package webagent

import (
	"context"
	"sync"

	"liveagent/internal/domain"
	"liveagent/internal/sandbox"
)

// Env implements domain.Environment and embodiment.BodySource.
type Env struct {
	mu  sync.Mutex
	box *sandbox.Docker

	task        string
	startURL    string
	successFile string
	successMin  int
	maxSteps    int64
	budgetInit  float64

	tick         int64
	step         int64
	episodeStart int64
	budget       float64

	lastAction   string
	lastStdout   string
	lastStderr   string
	lastExit     int
	lastTimedOut bool
}

// Params configures the web/code agent environment.
type Params struct {
	Task        string
	StartURL    string
	SuccessFile string
	SuccessMin  int
	MaxSteps    int64
	Budget      float64
}

// New builds the environment with a Docker sandbox.
func New(box *sandbox.Docker, p Params) *Env {
	if p.SuccessFile == "" {
		p.SuccessFile = "results.json"
	}
	if p.SuccessMin == 0 {
		p.SuccessMin = 1
	}
	if p.MaxSteps == 0 {
		p.MaxSteps = 8
	}
	if p.Budget == 0 {
		p.Budget = 12
	}
	return &Env{
		box: box, task: p.Task, startURL: p.StartURL, successFile: p.SuccessFile,
		successMin: p.SuccessMin, maxSteps: p.MaxSteps, budgetInit: p.Budget, budget: p.Budget,
	}
}

// Tick returns the current tick.
func (e *Env) Tick() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tick
}

// BodyState exposes the "resource budget" as a body field for embodiment.
func (e *Env) BodyState() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{
		"budget": e.budget,
		"steps":  float64(e.step),
	}
}

// AvailableActions is the registry whitelist for this environment.
func (e *Env) AvailableActions(_ context.Context) []domain.ActionSchema {
	return []domain.ActionSchema{
		{Name: "run_python", Description: "Run Python 3 code in the sandbox (network enabled, cwd is /work). Use for crawling/parsing.", Parameters: map[string]domain.ParamSpec{
			"code": {Type: "string", Required: true, Description: "the Python source to execute"},
		}},
		{Name: "run_shell", Description: "Run a shell script in the sandbox.", Parameters: map[string]domain.ParamSpec{
			"script": {Type: "string", Required: true, Description: "the shell script to execute"},
		}},
		{Name: "read_file", Description: "Read a file from the workspace.", Parameters: map[string]domain.ParamSpec{
			"path": {Type: "string", Required: true, Description: "path relative to the workspace"},
		}},
		{Name: "write_file", Description: "Write a file into the workspace.", Parameters: map[string]domain.ParamSpec{
			"path":    {Type: "string", Required: true, Description: "path relative to the workspace"},
			"content": {Type: "string", Required: true, Description: "file contents"},
		}},
		{Name: "finish", Description: "Declare the task complete.", Parameters: map[string]domain.ParamSpec{}},
	}
}

// Observe returns the current state, including the last run's output so the agent
// can debug and iterate on its own code.
func (e *Env) Observe(_ context.Context) (domain.Observation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.observation(), nil
}

func (e *Env) observation() domain.Observation {
	return domain.Observation{
		Tick: e.tick,
		Data: map[string]any{
			"task":              e.task,
			"start_url":         e.startURL,
			"success_file":      e.successFile,
			"success_min":       e.successMin,
			"workspace_files":   e.listFiles(),
			"last_action":       e.lastAction,
			"last_stdout":       e.lastStdout,
			"last_stderr":       e.lastStderr,
			"last_exit_code":    e.lastExit,
			"last_timed_out":    e.lastTimedOut,
			"budget_left":       e.budget,
			"step":              e.step,
			"records_collected": e.recordCount(),
		},
		Text: "web/code agent state",
	}
}
