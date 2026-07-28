// Package agent is the cognitive loop: a hybrid, reactive, native-tool-use
// controller. Each step it rebuilds a layered prompt (system + dynamic context
// block + rolling transcript + ask), asks the model for exactly one tool call,
// executes it through the safety-gated toolset, feeds the real result back, and
// repeats until the model calls finish, a limit trips, or work stalls.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/llm"
	"liveagent/internal/safety"
	"liveagent/internal/store"
	"liveagent/internal/tool"
)

// Deps are the agent's collaborators.
type Deps struct {
	LLM             domain.LLM
	Embedder        llm.Embedder // optional; nil disables semantic recall
	Tools           *tool.Toolset
	Store           *store.Store
	Guard           *safety.Guard
	Config          *config.Config
	Logger          *slog.Logger
	Workdir         string
	PromptExtension string
}

// Agent runs tasks. One Agent may run many tasks; per-task state is reset in Run.
type Agent struct {
	llm      domain.LLM
	embedder llm.Embedder
	tools    *tool.Toolset
	store    *store.Store
	guard    *safety.Guard
	cfg      *config.Config
	logger   *slog.Logger

	workdir         string
	promptExtension string

	// per-run state
	tx        *transcript
	task      string
	env       map[string]any
	plan      planItems
	lessons   []string // recalled lesson texts injected this run
	lessonIDs []int64  // note ids behind a.lessons, for reinforcement

	stallStreak      int             // consecutive unproductive steps
	seenOutputs      map[string]bool // output fingerprints, for novelty
	verifyRejections int             // times a finish was rejected by the verifier
}

// New builds an Agent.
func New(d Deps) *Agent {
	cfg := d.Config
	if cfg == nil {
		cfg = config.Default()
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		llm: d.LLM, embedder: d.Embedder, tools: d.Tools, store: d.Store, guard: d.Guard,
		cfg: cfg, logger: logger, workdir: d.Workdir, promptExtension: d.PromptExtension,
	}
}

// RunResult reports how a task run ended.
type RunResult struct {
	EpisodeID  string
	Steps      int
	Finished   bool
	Success    bool
	Verified   bool // an independent verifier confirmed the success claim
	Summary    string
	StopReason string // "finish" | "finish_unverified" | "max_steps" | "repeat" | "errors"
}

// Run drives one task to completion (or a limit).
func (a *Agent) Run(ctx context.Context, task string) (RunResult, error) {
	a.task = strings.TrimSpace(task)
	a.env = probeEnvironment(a.workdir)
	a.plan = nil
	a.stallStreak = 0
	a.verifyRejections = 0
	a.seenOutputs = map[string]bool{}
	a.tx = newTranscript(a.cfg.Context.MaxChars, a.cfg.Context.KeepRecent, a.cfg.Context.CompactAtPct)
	a.lessons = a.recallLessons(ctx)

	epID, err := a.store.StartEpisode(ctx, a.cfg.Agent.Name, a.task)
	if err != nil {
		return RunResult{}, err
	}
	a.logger.Info("run.start", "episode", epID, "task", a.task, "os", a.env["os"], "lessons", len(a.lessons))

	var lastKey string
	repeats, errStreak := 0, 0
	maxSteps := a.cfg.Limits.MaxSteps

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			_ = a.store.EndEpisode(ctx, epID, "cancelled", "context cancelled", step-1)
			return RunResult{EpisodeID: epID, Steps: step - 1, StopReason: "cancelled"}, err
		}

		a.tx.compactIfNeeded(ctx, a.summarize)
		req := domain.LLMRequest{
			Messages:    a.tx.build(a.systemPrompt(), a.buildAsk()),
			Tools:       a.tools.Defs(),
			ToolChoice:  "auto",
			Temperature: a.cfg.LLM.Temperature,
			MaxTokens:   a.cfg.LLM.MaxTokens,
		}

		resp, err := a.llm.Generate(ctx, req)
		if err != nil {
			a.logger.Warn("llm.error", "step", step, "err", err.Error())
			a.tx.addResult("LLM call failed: "+err.Error(), true)
			_ = a.store.RecordEvent(ctx, epID, step, "(llm_error)", nil, err.Error(), true)
			if errStreak++; errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
				return a.stop(ctx, epID, step, "errors", "too many consecutive LLM errors"), nil
			}
			continue
		}

		tc, ok := firstToolCall(resp)
		if !ok {
			// Some models (especially via gateways) emit the call as TEXT that
			// imitates the transcript ("TOOL_CALL <name> <json>") instead of a
			// native tool_call. Recover a registered call from the text before
			// giving up, so those turns still do real work.
			if rec, rok := a.recoverToolCall(resp.Text); rok {
				tc, ok = rec, true
				a.logger.Info("recovered_tool_call", "step", step, "tool", tc.Name)
			}
		}
		if !ok {
			nudge := "No tool was called. You MUST advance by calling exactly one tool."
			a.logger.Warn("no_tool_call", "step", step, "text", truncate(resp.Text, 160))
			a.tx.turns = append(a.tx.turns, domain.Message{Role: "assistant", Content: strings.TrimSpace(resp.Text)})
			a.tx.addResult(nudge, true)
			_ = a.store.RecordEvent(ctx, epID, step, "(no_tool_call)", nil, resp.Text, true)
			if errStreak++; errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
				return a.stop(ctx, epID, step, "errors", "model kept replying without calling a tool"), nil
			}
			continue
		}

		act := domain.Action{Name: tc.Name, Parameters: parseArgs(tc.Arguments)}

		if dec := a.guard.Check(act); !dec.Allowed {
			a.logger.Warn("safety.block", "step", step, "tool", tc.Name, "reason", dec.Reason)
			a.tx.addAction(tc.Name, tc.Arguments)
			a.tx.addResult("BLOCKED by safety policy: "+dec.Reason, true)
			_ = a.store.RecordEvent(ctx, epID, step, tc.Name, act.Parameters, "BLOCKED: "+dec.Reason, true)
			if errStreak++; errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
				return a.stop(ctx, epID, step, "errors", "repeatedly attempted blocked actions"), nil
			}
			continue
		}

		// Anti-spam: detect the exact same call repeated in a row.
		key := tc.Name + "|" + normalizeArgs(act.Parameters)
		if key == lastKey {
			repeats++
		} else {
			repeats, lastKey = 1, key
		}

		res := a.tools.Execute(ctx, act)
		a.logger.Info("step", "n", step, "tool", tc.Name, "error", res.IsError, "out", truncate(res.Output, 200))
		a.tx.addAction(tc.Name, tc.Arguments)
		a.tx.addResult(res.Output, res.IsError)
		_ = a.store.RecordEvent(ctx, epID, step, tc.Name, act.Parameters, res.Output, res.IsError)

		if res.HasPlan {
			a.plan = res.Plan
		}
		if res.IsError {
			errStreak++
		} else {
			errStreak = 0
		}
		a.updateStall(tc.Name, res)

		if res.Finished {
			// Verify a positive success claim independently before trusting it.
			if res.Success && a.cfg.Verify.On() {
				verified, decided, reason := a.verifyFinish(ctx, res.Output)
				if decided && !verified {
					if a.verifyRejections < a.cfg.Verify.MaxRejections {
						a.verifyRejections++
						a.logger.Warn("finish.rejected", "step", step, "reason", reason, "n", a.verifyRejections)
						a.tx.addResult("FINISH REJECTED by an independent verifier: "+reason+
							" Keep working; only call finish when the success criteria are truly met and the evidence exists.", true)
						continue
					}
					_ = a.store.EndEpisode(ctx, epID, "finished_unverified", res.Output, step)
					a.logger.Warn("run.finish_unverified", "episode", epID, "reason", reason, "steps", step)
					return RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: false, Verified: false,
						Summary: res.Output + " [UNVERIFIED: " + reason + "]", StopReason: "finish_unverified"}, nil
				}
				_ = a.store.EndEpisode(ctx, epID, "success", res.Output, step)
				a.logger.Info("run.finish", "episode", epID, "success", true, "verified", decided, "steps", step)
				if decided && verified {
					a.creditLessons(ctx)             // reward lessons that were in play
					a.distillLesson(ctx, res.Output) // learn only from grounded success
					a.evictWeakLessons(ctx)          // prune chronically-unhelpful lessons
				}
				return RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: true, Verified: decided && verified,
					Summary: res.Output, StopReason: "finish"}, nil
			}
			status := "finished"
			if res.Success {
				status = "success"
			}
			_ = a.store.EndEpisode(ctx, epID, status, res.Output, step)
			a.logger.Info("run.finish", "episode", epID, "success", res.Success, "steps", step)
			return RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: res.Success, Summary: res.Output, StopReason: "finish"}, nil
		}
		if repeats >= a.cfg.Limits.MaxRepeats {
			return a.stop(ctx, epID, step, "repeat", fmt.Sprintf("same action %q repeated %d times", tc.Name, repeats)), nil
		}
		if errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
			return a.stop(ctx, epID, step, "errors", "too many consecutive failing steps"), nil
		}
	}

	return a.stop(ctx, epID, maxSteps, "max_steps", "reached step limit"), nil
}

func (a *Agent) stop(ctx context.Context, epID string, step int, reason, detail string) RunResult {
	_ = a.store.EndEpisode(ctx, epID, "stopped:"+reason, detail, step)
	a.logger.Warn("run.stop", "episode", epID, "reason", reason, "detail", detail, "steps", step)
	return RunResult{EpisodeID: epID, Steps: step, StopReason: reason, Summary: detail}
}

// updateStall tracks unproductive steps: an error, or byte-identical repeated
// output, is unproductive; a novel successful result resets the streak. Meta
// tools (update_plan/finish) do not affect it.
func (a *Agent) updateStall(toolName string, res tool.Result) {
	if toolName == "update_plan" || toolName == "finish" {
		return
	}
	key := strings.TrimSpace(res.Output)
	novel := key != "" && !a.seenOutputs[key]
	if key != "" {
		a.seenOutputs[key] = true
	}
	if res.IsError || !novel {
		a.stallStreak++
	} else {
		a.stallStreak = 0
	}
}

// summarize is the LLM-backed compaction summarizer.
func (a *Agent) summarize(ctx context.Context, text string) string {
	req := domain.LLMRequest{
		Messages: []domain.Message{
			{Role: "system", Content: "You compress an agent's step history. In under 120 words, summarise what was attempted, which approach/code was tried, and what errors or results occurred. Plain text only."},
			{Role: "user", Content: text},
		},
		Temperature: 0.2,
		MaxTokens:   256,
	}
	resp, err := a.llm.Generate(ctx, req)
	if err != nil {
		return ""
	}
	return resp.Text
}

func firstToolCall(resp domain.LLMResponse) (domain.ToolCall, bool) {
	for _, tc := range resp.ToolCalls {
		if strings.TrimSpace(tc.Name) != "" {
			return tc, true
		}
	}
	return domain.ToolCall{}, false
}

// recoverToolCall extracts a REGISTERED tool call from free text. It handles the
// "TOOL_CALL <name> <json-args>" transcript-echo pattern and a generic
// {"name"|"tool"|"action": ..., "arguments"|"parameters": {...}} object. Only
// names present in the toolset are accepted, so a model merely talking about a
// tool (or printing JSON output) is not mistaken for a call.
func (a *Agent) recoverToolCall(text string) (domain.ToolCall, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return domain.ToolCall{}, false
	}

	if idx := strings.Index(t, "TOOL_CALL"); idx >= 0 {
		rest := strings.TrimSpace(t[idx+len("TOOL_CALL"):])
		name := rest
		if sp := strings.IndexAny(rest, " \t\n{"); sp >= 0 {
			name = strings.TrimSpace(rest[:sp])
		}
		if a.tools.Has(name) {
			args := ""
			if obj, err := llm.ExtractJSONObject(rest); err == nil {
				args = obj
			}
			return domain.ToolCall{Name: name, Arguments: args}, true
		}
	}

	if obj, err := llm.ExtractJSONObject(t); err == nil {
		var m map[string]any
		if json.Unmarshal([]byte(obj), &m) == nil {
			name := firstString(m, "name", "tool", "action", "function")
			if name != "" && a.tools.Has(name) {
				args := ""
				if v, ok := m["arguments"]; ok {
					args = toJSON(v)
				} else if v, ok := m["parameters"]; ok {
					args = toJSON(v)
				}
				return domain.ToolCall{Name: name, Arguments: args}, true
			}
		}
	}
	return domain.ToolCall{}, false
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toJSON(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseArgs(arguments string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(arguments) == "" {
		return out
	}
	if obj, err := llm.ExtractJSONObject(arguments); err == nil {
		_ = json.Unmarshal([]byte(obj), &out)
	}
	return out
}

// normalizeArgs produces a stable string for repeat detection.
func normalizeArgs(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	b, _ := json.Marshal(params)
	return string(b)
}
