// Package agent is the cognitive loop: a hybrid, reactive, native-tool-use
// controller. Each step it rebuilds a layered prompt (system + dynamic context
// block + rolling transcript + ask), asks the model for exactly one tool call,
// executes it through the safety-gated toolset, feeds the real result back, and
// repeats until the model calls finish, a limit trips, or work stalls.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	OnEvent         Emitter // optional; receives structured progress events

	// Evaluation toggles (default false). RecallDisabled skips injecting recalled
	// lessons; LearnDisabled skips distilling/crediting/evicting notes. Used by the
	// offline A/B harness to measure the learning loop's effect in isolation.
	RecallDisabled bool
	LearnDisabled  bool
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
	onEvent         Emitter
	recallOff       bool
	learnOff        bool

	// per-session state (persists across conversational turns)
	tx        *transcript
	task      string // the session's OPENING goal (first user message) — used for the title, not as the live objective
	turnTask  string // the CURRENT turn's request (latest user message); what the prompt + verifier judge against
	env       map[string]any
	plan      planItems
	lessons   []string // recalled lesson texts injected this session
	lessonIDs []int64  // note ids behind a.lessons, for reinforcement
	epID      string   // episode/session id; one id for the whole conversation
	step      int      // monotonically increasing step counter across turns
	titleSet  bool     // whether the session already has a title (LLM- or auto-set)

	// per-turn state (reset at the start of each turn)
	stallStreak      int             // consecutive unproductive steps
	seenOutputs      map[string]bool // output fingerprints, for novelty
	verifyRejections int             // times a finish was rejected by the verifier
	pendingImages    []string        // data-URI images to attach to the first LLM call

	// per-turn usage telemetry (reset at the start of each turn)
	turnStart      time.Time
	turnPrompt     int // cumulative prompt (input) tokens this turn
	turnCompletion int // cumulative completion (output) tokens this turn
	turnTokens     int // cumulative total tokens this turn
	turnCalls      int // number of LLM calls this turn (incl. verify/summarize/title)
}

// Attachment is a file the user attached to a turn. It already lives in the
// session workspace (under ./uploads). Image marks files that should be sent to
// a vision-capable model as image parts (decided by the caller per settings).
type Attachment struct {
	Name  string // path relative to the workspace, e.g. "uploads/photo.png"
	Path  string // absolute path on disk
	Mime  string
	Image bool
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
		onEvent: d.OnEvent, recallOff: d.RecallDisabled, learnOff: d.LearnDisabled,
	}
}

// recall returns recalled lessons unless recall is disabled (eval arm B).
func (a *Agent) recall(ctx context.Context) []string {
	if a.recallOff {
		return nil
	}
	return a.recallLessons(ctx)
}

// SetLLM swaps the cognitive model for this agent. The web layer uses it to
// honour a per-session (or per-turn) model choice from the UI.
func (a *Agent) SetLLM(m domain.LLM) {
	if m != nil {
		a.llm = m
	}
}

// SetEmitter rebinds the progress emitter. A long-lived session's agent is
// reused across HTTP turns, each with its own response stream, so the emitter
// must be swapped in before every turn.
func (a *Agent) SetEmitter(e Emitter) { a.onEvent = e }

// RunResult reports how a turn ended.
type RunResult struct {
	EpisodeID  string
	Steps      int
	Finished   bool
	Success    bool
	Verified   bool // an independent verifier confirmed the success claim
	Reply      bool // the turn ended with a direct conversational answer
	Summary    string
	StopReason string // "finish" | "finish_unverified" | "reply" | "max_steps" | "repeat" | "errors"

	// usage telemetry for the whole turn
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LLMCalls         int
	ElapsedMS        int64
}

// Run drives one task to completion in a fresh, single-turn session. Kept for
// the CLI and tests; the web layer uses StartSession + RunTurn for long-lived,
// multi-turn conversations.
func (a *Agent) Run(ctx context.Context, task string) (RunResult, error) {
	if _, err := a.StartSession(ctx, "", task); err != nil {
		return RunResult{}, err
	}
	return a.RunTurn(ctx, task), nil
}

// StartSession begins a fresh, persistent conversation under a caller-chosen id
// (empty auto-generates one). All session state — transcript, workspace, plan,
// recalled lessons — survives across turns, so the conversation keeps going
// after a task finishes or a question is answered.
func (a *Agent) StartSession(ctx context.Context, id, firstTask string) (string, error) {
	a.task = strings.TrimSpace(firstTask)
	a.turnTask = a.task // until the first RunTurn sets the live request
	a.env = probeEnvironment(a.workdir)
	a.plan = nil
	a.seenOutputs = map[string]bool{}
	a.step = 0
	a.titleSet = false
	a.tx = newTranscript(a.cfg.Context.MaxChars, a.cfg.Context.KeepRecent, a.cfg.Context.CompactAtPct)
	a.lessons = a.recall(ctx)

	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("ep_%d", time.Now().UnixNano())
	}
	if err := a.store.StartEpisodeID(ctx, id, a.cfg.Agent.Name, a.task); err != nil {
		return "", err
	}
	a.epID = id
	osStr, _ := a.env["os"].(string)
	a.logger.Info("session.start", "episode", id, "task", a.task, "os", osStr, "lessons", len(a.lessons))
	a.emit(Event{Type: "start", EpisodeID: id, Text: a.task, Lessons: len(a.lessons), OS: osStr})
	return id, nil
}

// Rehydrate rebuilds an existing session's in-memory state from stored events
// so the conversation can continue after the live session was lost (e.g. a
// server restart). The workspace directory is expected to still exist on disk.
func (a *Agent) Rehydrate(ctx context.Context, id, task string, events []store.EventRow) {
	a.task = strings.TrimSpace(task)
	a.turnTask = a.task // overwritten by the next RunTurn's user message
	a.env = probeEnvironment(a.workdir)
	a.plan = nil
	a.seenOutputs = map[string]bool{}
	a.tx = newTranscript(a.cfg.Context.MaxChars, a.cfg.Context.KeepRecent, a.cfg.Context.CompactAtPct)
	a.epID = id
	a.lessons = a.recall(ctx)

	maxStep := 0
	for _, e := range events {
		if e.Step > maxStep {
			maxStep = e.Step
		}
		switch e.Tool {
		case "(user)":
			a.tx.addUser(e.Result)
		case "(assistant)":
			a.tx.turns = append(a.tx.turns, domain.Message{Role: "assistant", Content: e.Result})
		case "update_plan", "finish", "set_title", "(no_tool_call)", "(llm_error)":
			// meta / noise: skip
		default:
			a.tx.addAction(e.Tool, e.Args)
			a.tx.addResult(e.Result, e.IsError)
		}
	}
	a.step = maxStep
	osStr, _ := a.env["os"].(string)
	a.logger.Info("session.rehydrate", "episode", id, "events", len(events), "step", maxStep)
	a.emit(Event{Type: "start", EpisodeID: id, Text: a.task, Lessons: len(a.lessons), OS: osStr})
}

// sayMessage surfaces the model's prose to observers and records it so the
// conversation can be replayed later. When streamed is true the prose was
// already shown live via delta events, so it is only recorded (not re-emitted).
func (a *Agent) sayMessage(ctx context.Context, step int, text string, streamed bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if !streamed {
		a.emit(Event{Type: "message", Step: step, Text: truncate(text, maxEventText)})
	}
	_ = a.store.RecordEvent(ctx, a.epID, step, "(assistant)", nil, text, false)
}

// maxVisionBytes bounds a single image sent to the model (as base64), and
// maxVisionImages caps how many are sent per turn, to keep requests sane.
const (
	maxVisionBytes  = 5 << 20
	maxVisionImages = 6
)

// prepareAttachments turns image attachments (marked Image=true and small
// enough) into base64 data URIs for the first LLM call. Non-image or oversized
// files are left as workspace files the tools can read.
func (a *Agent) prepareAttachments(atts []Attachment) []string {
	var out []string
	for _, at := range atts {
		if !at.Image || len(out) >= maxVisionImages {
			continue
		}
		data, err := os.ReadFile(at.Path)
		if err != nil || len(data) == 0 || len(data) > maxVisionBytes {
			continue
		}
		mt := at.Mime
		if mt == "" {
			mt = "image/" + strings.TrimPrefix(strings.ToLower(filepath.Ext(at.Name)), ".")
		}
		out = append(out, "data:"+mt+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return out
}

// attachmentNote is a short line listing attached files so the agent knows they
// exist (and where) even when they are not sent as images.
func attachmentNote(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	names := make([]string, 0, len(atts))
	for _, at := range atts {
		names = append(names, at.Name)
	}
	return "[The user attached these files, saved in the workspace: " + strings.Join(names, ", ") +
		". Read them with read_file (or the image/pdf tools) as needed.]"
}

func isImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// MarkTitled records that the session already has a title (e.g. a rehydrated
// session that was named in a previous life), so EnsureTitle is a no-op.
func (a *Agent) MarkTitled() { a.titleSet = true }

// EnsureTitle guarantees the session has a short, LLM-authored title. If the
// model already named it via set_title this is a no-op; otherwise it makes one
// small LLM call to summarise the topic, stores it and emits a title event.
// hint is the current turn's summary/answer, used as extra context.
func (a *Agent) EnsureTitle(ctx context.Context, hint string) {
	if a.titleSet || a.store == nil {
		return
	}
	sys := "Produce a concise conversation title of 3-6 words summarising the topic. " +
		"Reply with ONLY the title text: no quotes, no trailing punctuation, no prefix."
	user := "First user message:\n" + a.task
	if h := strings.TrimSpace(hint); h != "" {
		user += "\n\nAssistant response:\n" + truncate(h, 600)
	}
	resp, err := a.llm.Generate(ctx, domain.LLMRequest{
		Messages: []domain.Message{{Role: "system", Content: sys}, {Role: "user", Content: user}},
		Temperature: 0.3,
		// Give reasoning models room to think AND still emit the title as content;
		// too small a budget gets consumed entirely by hidden reasoning.
		MaxTokens: 256,
	})
	if err != nil {
		return
	}
	a.addUsage(a.step, resp)
	title := strings.TrimSpace(resp.Text)
	if title == "" { // reasoning-only reply: fall back to the last thinking line
		if rz := strings.TrimSpace(resp.Reasoning); rz != "" {
			lines := strings.Split(rz, "\n")
			title = strings.TrimSpace(lines[len(lines)-1])
		}
	}
	title = strings.Trim(title, "\"'“”")
	if i := strings.IndexAny(title, "\n\r"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if title == "" {
		return
	}
	if len(title) > 120 {
		title = title[:120]
	}
	a.titleSet = true
	_ = a.store.SetEpisodeTitle(ctx, a.epID, title)
	a.emit(Event{Type: "title", Step: a.step, Text: title})
	a.logger.Info("session.title.auto", "episode", a.epID, "title", title)
}

// addUsage folds one LLM response's token usage into the turn totals and emits
// a cumulative "usage" event so the UI can show a live token/latency readout.
// It is called for every model call in a turn (loop steps, verify, summarize,
// title) so the cost reflects the full turn, not just the main loop.
func (a *Agent) addUsage(step int, resp domain.LLMResponse) {
	a.turnCalls++
	a.turnPrompt += resp.PromptTokens
	a.turnCompletion += resp.CompletionTokens
	if resp.TokensUsed > 0 {
		a.turnTokens += resp.TokensUsed
	} else {
		a.turnTokens += resp.PromptTokens + resp.CompletionTokens
	}
	a.emit(Event{
		Type: "usage", Step: step,
		PromptTokens: a.turnPrompt, CompletionTokens: a.turnCompletion, TotalTokens: a.turnTokens,
		LLMCalls: a.turnCalls, ElapsedMS: time.Since(a.turnStart).Milliseconds(),
	})
}

// withUsage stamps the turn's cumulative usage telemetry onto a RunResult.
func (a *Agent) withUsage(r RunResult) RunResult {
	r.PromptTokens = a.turnPrompt
	r.CompletionTokens = a.turnCompletion
	r.TotalTokens = a.turnTokens
	r.LLMCalls = a.turnCalls
	if !a.turnStart.IsZero() {
		r.ElapsedMS = time.Since(a.turnStart).Milliseconds()
	}
	return r
}

// generate runs the model for one step. If the provider supports streaming,
// reasoning/prose tokens are emitted as delta events as they arrive and the
// returned streamed flag is true (so the caller does not re-emit the full text).
// Otherwise it falls back to a single blocking call.
func (a *Agent) generate(ctx context.Context, req domain.LLMRequest, step int) (domain.LLMResponse, bool, error) {
	s, ok := a.llm.(domain.StreamingLLM)
	if !ok {
		resp, err := a.llm.Generate(ctx, req)
		return resp, false, err
	}
	streamed := false
	resp, err := s.GenerateStream(ctx, req, func(d domain.StreamDelta) {
		if d.Reasoning != "" {
			streamed = true
			a.emit(Event{Type: "delta", Kind: "think", Step: step, Text: d.Reasoning})
		}
		if d.Content != "" {
			streamed = true
			a.emit(Event{Type: "delta", Kind: "message", Step: step, Text: d.Content})
		}
	})
	return resp, streamed, err
}

// RunTurn advances the session by one user message: it records the message,
// then loops (one tool per step) until the model answers with reply, calls
// finish, or a limit trips. Session state persists so the next turn continues
// with full context.
func (a *Agent) RunTurn(ctx context.Context, userMsg string, attachments ...Attachment) RunResult {
	userMsg = strings.TrimSpace(userMsg)

	// The live objective for this turn is the latest user message — NOT the
	// session's opening goal. Anchoring the prompt/verifier to userMsg is what
	// keeps a long, multi-task conversation from drifting back to an earlier task.
	if userMsg != "" {
		a.turnTask = userMsg
	}

	// The plan is a per-turn scratchpad: the previous turn already returned
	// (finished/answered/hit a limit), so its TODO list is stale. Clearing it
	// forces a fresh plan for the new request and stops old tasks' plan items
	// from bleeding into (and reviving) unrelated work. History still lives in
	// the transcript, so a genuine continuation can be re-planned.
	a.plan = nil

	// per-turn reset
	a.stallStreak = 0
	a.verifyRejections = 0
	a.pendingImages = a.prepareAttachments(attachments)
	a.turnStart = time.Now()
	a.turnPrompt, a.turnCompletion, a.turnTokens, a.turnCalls = 0, 0, 0, 0

	// Fold an attachment note into the recorded user message so the agent (and
	// history replay) knows which files are available under ./uploads.
	recorded := userMsg
	if note := attachmentNote(attachments); note != "" {
		recorded = strings.TrimSpace(userMsg + "\n\n" + note)
	}
	a.tx.addUser(recorded)
	_ = a.store.RecordEvent(ctx, a.epID, a.step, "(user)", nil, recorded, false)
	var lastKey string
	repeats, errStreak := 0, 0
	maxSteps := a.cfg.Limits.MaxSteps
	epID := a.epID

	for i := 1; i <= maxSteps; i++ {
		if err := ctx.Err(); err != nil {
			_ = a.store.EndEpisode(ctx, epID, "cancelled", "context cancelled", a.step)
			return a.withUsage(RunResult{EpisodeID: epID, Steps: a.step, StopReason: "cancelled"})
		}
		a.step++
		step := a.step

		a.tx.compactIfNeeded(ctx, a.summarize)
		req := domain.LLMRequest{
			Messages:    a.tx.build(a.systemPrompt(), a.buildAsk()),
			Tools:       a.tools.Defs(),
			ToolChoice:  "auto",
			Temperature: a.cfg.LLM.Temperature,
			MaxTokens:   a.cfg.LLM.MaxTokens,
		}
		// Attach any pending images to the current (last) user message, once, on
		// the first step of the turn. Consumed immediately so later steps don't
		// resend the (large) image payloads.
		imagesAttached := false
		if len(a.pendingImages) > 0 {
			last := len(req.Messages) - 1
			req.Messages[last].Images = a.pendingImages
			imagesAttached = true
			a.pendingImages = nil
		}

		resp, streamed, err := a.generate(ctx, req, step)
		// Auto-degrade: if a request carrying images failed (e.g. the model has no
		// vision), retry once as text-only. The images remain available as files.
		if err != nil && imagesAttached {
			a.logger.Warn("llm.vision_retry", "step", step, "err", err.Error())
			a.emit(Event{Type: "info", Step: step, Text: "model could not accept the image; retrying without it (the file is still available to tools)"})
			req.Messages[len(req.Messages)-1].Images = nil
			resp, streamed, err = a.generate(ctx, req, step)
		}
		if err == nil {
			a.addUsage(step, resp)
		}
		if err != nil {
			a.logger.Warn("llm.error", "step", step, "err", err.Error())
			a.tx.addResult("LLM call failed: "+err.Error(), true)
			_ = a.store.RecordEvent(ctx, epID, step, "(llm_error)", nil, err.Error(), true)
			a.emit(Event{Type: "warn", Step: step, Text: "LLM call failed: " + err.Error(), IsError: true})
			if errStreak++; errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
				return a.stop(ctx, epID, step, "errors", "too many consecutive LLM errors")
			}
			continue
		}

		// Surface the model's reasoning ("thinking") for this step, when the
		// provider exposes it, so observers can watch how it decides. When
		// streaming, the reasoning was already sent live as delta events.
		if rz := strings.TrimSpace(resp.Reasoning); rz != "" && !streamed {
			a.emit(Event{Type: "think", Step: step, Text: truncate(rz, maxEventText)})
		}

		// Collect ALL tool calls the model emitted this step. A single response
		// may carry several (native parallel tool-calling); we execute read-only
		// ones concurrently and keep the rest ordered (see executeBatch).
		calls := toolCalls(resp)
		if len(calls) == 0 {
			// Some models (especially via gateways) emit the call as TEXT that
			// imitates the transcript ("TOOL_CALL <name> <json>") instead of a
			// native tool_call. Recover a registered call from the text.
			if rec, rok := a.recoverToolCall(resp.Text); rok {
				calls = []domain.ToolCall{rec}
				a.logger.Info("recovered_tool_call", "step", step, "tool", rec.Name)
				a.emit(Event{Type: "info", Step: step, Text: "recovered tool call from text: " + rec.Name})
			}
		}

		// Accompanying prose is a real response. For reply the answer comes from
		// the tool result itself, so skip the prose to avoid showing it twice.
		hasReply := false
		for _, c := range calls {
			if c.Name == "reply" {
				hasReply = true
			}
		}
		if len(calls) > 0 && !hasReply {
			a.sayMessage(ctx, step, resp.Text, streamed)
		}

		if len(calls) == 0 {
			a.sayMessage(ctx, step, resp.Text, streamed)
			nudge := "No tool was called. You MUST advance by calling at least one tool (use reply to answer the user directly)."
			a.logger.Warn("no_tool_call", "step", step, "text", truncate(resp.Text, 160))
			a.tx.addResult(nudge, true)
			_ = a.store.RecordEvent(ctx, epID, step, "(no_tool_call)", nil, resp.Text, true)
			a.emit(Event{Type: "warn", Step: step, Text: "model replied without calling a tool", IsError: true})
			if errStreak++; errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
				return a.stop(ctx, epID, step, "errors", "model kept replying without calling a tool")
			}
			continue
		}

		// Anti-spam: detect the exact same batch of calls repeated in a row.
		key := batchKey(calls)
		if key == lastKey {
			repeats++
		} else {
			repeats, lastKey = 1, key
		}

		// Execute the whole batch: read-only calls in parallel, mutating calls in
		// order, terminal calls (finish/reply) last so a finish sees their effects.
		results := a.executeBatch(ctx, step, calls)

		// Record + surface each call in the order the model emitted them.
		anyOK := false
		termIdx := -1
		for i, c := range calls {
			r := results[i]
			a.tx.addAction(c.Name, c.Arguments)
			a.tx.addResult(r.res.Output, r.res.IsError)
			_ = a.store.RecordEvent(ctx, epID, step, c.Name, r.act.Parameters, r.res.Output, r.res.IsError)
			if c.Name != "finish" && c.Name != "reply" && c.Name != "set_title" && c.Name != "send_file" {
				a.emit(Event{Type: "step", Step: step, Tool: c.Name, Args: r.act.Parameters, Output: r.res.Output, IsError: r.res.IsError})
			}
			if sf := strings.TrimSpace(r.res.SendFile); sf != "" && !r.res.IsError {
				a.emit(Event{Type: "file", Step: step, Args: map[string]any{
					"name": sf, "image": isImageName(sf), "caption": r.res.Caption,
				}})
				a.logger.Info("session.send_file", "episode", epID, "file", sf)
			}
			if r.res.HasPlan {
				a.plan = r.res.Plan
				a.emit(Event{Type: "plan", Step: step, Plan: r.res.Plan})
			}
			if t := strings.TrimSpace(r.res.Title); t != "" {
				_ = a.store.SetEpisodeTitle(ctx, epID, t)
				a.titleSet = true
				a.emit(Event{Type: "title", Step: step, Text: t})
				a.logger.Info("session.title", "episode", epID, "title", t)
			}
			a.updateStall(c.Name, r.res)
			if !r.res.IsError {
				anyOK = true
			}
			if (c.Name == "reply" || c.Name == "finish") && !r.blocked && termIdx < 0 {
				termIdx = i
			}
		}
		if anyOK {
			errStreak = 0
		} else {
			errStreak++
		}

		// A direct conversational answer ends the turn without task verification;
		// the session stays open for whatever comes next.
		if termIdx >= 0 && calls[termIdx].Name == "reply" {
			res := results[termIdx].res
			_ = a.store.EndEpisode(ctx, epID, "idle", res.Output, a.step)
			a.emit(Event{Type: "message", Step: step, Text: truncate(res.Output, maxEventText)})
			a.logger.Info("turn.reply", "episode", epID, "steps", a.step)
			return a.withUsage(RunResult{EpisodeID: epID, Steps: a.step, Finished: true, Reply: true, Summary: res.Output, StopReason: "reply"})
		}

		if termIdx >= 0 && calls[termIdx].Name == "finish" {
			res := results[termIdx].res
			// Verify a positive success claim independently before trusting it.
			if res.Success && a.cfg.Verify.On() {
				verified, decided, reason := a.verifyFinish(ctx, res.Output)
				if decided && !verified {
					if a.verifyRejections < a.cfg.Verify.MaxRejections {
						a.verifyRejections++
						a.logger.Warn("finish.rejected", "step", step, "reason", reason, "n", a.verifyRejections)
						a.tx.addResult("FINISH REJECTED by an independent verifier: "+reason+
							" Keep working; only call finish when the success criteria are truly met and the evidence exists.", true)
						a.emit(Event{Type: "verify", Step: step, IsError: true, Text: "finish rejected by verifier: " + reason})
						continue
					}
					_ = a.store.EndEpisode(ctx, epID, "finished_unverified", res.Output, step)
					a.logger.Warn("run.finish_unverified", "episode", epID, "reason", reason, "steps", step)
					a.emit(Event{Type: "finish", Step: step, Success: false, Verified: false, StopReason: "finish_unverified",
						Summary: res.Output + " [UNVERIFIED: " + reason + "]"})
					return a.withUsage(RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: false, Verified: false,
						Summary: res.Output + " [UNVERIFIED: " + reason + "]", StopReason: "finish_unverified"})
				}
				_ = a.store.EndEpisode(ctx, epID, "success", res.Output, step)
				a.logger.Info("run.finish", "episode", epID, "success", true, "verified", decided, "steps", step)
				if decided && verified {
					a.emit(Event{Type: "verify", Step: step, Success: true, Verified: true, Text: "verifier confirmed success"})
					if !a.learnOff {
						a.creditLessons(ctx)             // reward lessons that were in play
						a.distillLesson(ctx, res.Output) // learn only from grounded success
						a.evictWeakLessons(ctx)          // prune chronically-unhelpful lessons
					}
				}
				a.emit(Event{Type: "finish", Step: step, Success: true, Verified: decided && verified, StopReason: "finish", Summary: res.Output})
				return a.withUsage(RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: true, Verified: decided && verified,
					Summary: res.Output, StopReason: "finish"})
			}
			status := "finished"
			if res.Success {
				status = "success"
			}
			_ = a.store.EndEpisode(ctx, epID, status, res.Output, step)
			a.logger.Info("run.finish", "episode", epID, "success", res.Success, "steps", step)
			a.emit(Event{Type: "finish", Step: step, Success: res.Success, Verified: false, StopReason: "finish", Summary: res.Output})
			return a.withUsage(RunResult{EpisodeID: epID, Steps: step, Finished: true, Success: res.Success, Summary: res.Output, StopReason: "finish"})
		}
		if repeats >= a.cfg.Limits.MaxRepeats {
			return a.stop(ctx, epID, step, "repeat", fmt.Sprintf("the same set of actions (%s) repeated %d times", batchNames(calls), repeats))
		}
		if errStreak >= a.cfg.Limits.MaxConsecutiveErrors {
			return a.stop(ctx, epID, step, "errors", "too many consecutive failing steps")
		}
	}

	return a.stop(ctx, epID, a.step, "max_steps", "reached step limit")
}

func (a *Agent) stop(ctx context.Context, epID string, step int, reason, detail string) RunResult {
	_ = a.store.EndEpisode(ctx, epID, "stopped:"+reason, detail, step)
	a.logger.Warn("run.stop", "episode", epID, "reason", reason, "detail", detail, "steps", step)
	a.emit(Event{Type: "stop", Step: step, StopReason: reason, Summary: detail})
	return a.withUsage(RunResult{EpisodeID: epID, Steps: step, StopReason: reason, Summary: detail})
}

// updateStall tracks unproductive steps: an error, or byte-identical repeated
// output, is unproductive; a novel successful result resets the streak. Meta
// tools (update_plan/finish) do not affect it.
func (a *Agent) updateStall(toolName string, res tool.Result) {
	if toolName == "update_plan" || toolName == "finish" || toolName == "set_title" || toolName == "send_file" {
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
	a.addUsage(a.step, resp)
	return resp.Text
}

// callResult pairs a parsed action with its execution outcome (aligned 1:1 with
// the calls slice passed to executeBatch).
type callResult struct {
	act     domain.Action
	res     tool.Result
	blocked bool // rejected by the safety guard before execution
}

// toolCalls returns every named tool call in a response (native parallel
// function-calling may emit more than one).
func toolCalls(resp domain.LLMResponse) []domain.ToolCall {
	var out []domain.ToolCall
	for _, tc := range resp.ToolCalls {
		if strings.TrimSpace(tc.Name) != "" {
			out = append(out, tc)
		}
	}
	return out
}

// parallelSafe reports whether a tool has no side effects and can run
// concurrently with others in the same batch. Everything else runs serially.
func parallelSafe(name string) bool {
	switch name {
	case "read_file", "list_dir", "glob", "grep", "http_fetch":
		return true
	}
	return false
}

func isTerminalTool(name string) bool { return name == "finish" || name == "reply" }

// batchKey is a stable signature of a batch of calls for repeat detection
// (order-independent, so re-emitting the same set in a different order counts).
func batchKey(calls []domain.ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = c.Name + "|" + normalizeArgs(parseArgs(c.Arguments))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func batchNames(calls []domain.ToolCall) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// executeBatch runs all calls from one model response and returns results in
// the same order. Read-only calls run concurrently; side-effecting calls run
// sequentially in emitted order (so writes are deterministic and cannot race);
// blocked calls are turned into error results; terminal calls (finish/reply)
// run last — and only the first one — so a finish observes the effects of the
// other calls before it verifies.
func (a *Agent) executeBatch(ctx context.Context, step int, calls []domain.ToolCall) []callResult {
	out := make([]callResult, len(calls))

	// Parse arguments and apply the safety guard up front.
	for i, c := range calls {
		act := domain.Action{Name: c.Name, Parameters: parseArgs(c.Arguments)}
		out[i].act = act
		if dec := a.guard.Check(act); !dec.Allowed {
			a.logger.Warn("safety.block", "step", step, "tool", c.Name, "reason", dec.Reason)
			out[i].res = tool.Result{IsError: true, Output: "BLOCKED by safety policy: " + dec.Reason}
			out[i].blocked = true
		}
	}

	// Only stream live stdout when a single shell/python call is present, so
	// concurrent commands don't interleave into one live output card.
	shellCount := 0
	for _, c := range calls {
		if c.Name == "run_shell" || c.Name == "run_python" {
			shellCount++
		}
	}

	// Phase 1: read-only, non-terminal, non-blocked calls run concurrently.
	var wg sync.WaitGroup
	for i, c := range calls {
		if out[i].blocked || isTerminalTool(c.Name) || !parallelSafe(c.Name) {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			out[idx].res = a.tools.Execute(ctx, out[idx].act)
			a.logger.Info("step", "n", step, "tool", out[idx].act.Name, "error", out[idx].res.IsError, "parallel", true)
		}(i)
	}

	// Phase 2: side-effecting, non-terminal, non-blocked calls run in order.
	for i, c := range calls {
		if out[i].blocked || isTerminalTool(c.Name) || parallelSafe(c.Name) {
			continue
		}
		ec := ctx
		if (c.Name == "run_shell" || c.Name == "run_python") && shellCount == 1 {
			ec = tool.WithOutputSink(ctx, func(chunk string) {
				a.emit(Event{Type: "stdout", Step: step, Tool: c.Name, Text: chunk})
			})
		}
		out[i].res = a.tools.Execute(ec, out[i].act)
		a.logger.Info("step", "n", step, "tool", c.Name, "error", out[i].res.IsError, "out", truncate(out[i].res.Output, 200))
	}
	wg.Wait()

	// Phase 3: terminal calls run last; only the first acts, the rest are no-ops.
	firstTerm := -1
	for i, c := range calls {
		if out[i].blocked || !isTerminalTool(c.Name) {
			continue
		}
		if firstTerm < 0 {
			firstTerm = i
			out[i].res = a.tools.Execute(ctx, out[i].act)
		} else {
			out[i].res = tool.Result{IsError: true, Output: fmt.Sprintf("ignored: %q already ended this turn", calls[firstTerm].Name)}
		}
	}
	return out
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
