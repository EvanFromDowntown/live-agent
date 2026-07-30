// Command server exposes the agent over a small web UI. It serves a single-page
// frontend and streams a run's progress (every tool call, result, plan update,
// verification and the final outcome) to the browser via Server-Sent Events, so
// you can watch the agent work and inspect the files it produces. Runs are
// serialised (one at a time) and each gets its own workspace subdirectory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"liveagent/internal/agent"
	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/llm"
	"liveagent/internal/safety"
	"liveagent/internal/store"
	"liveagent/internal/tool"
)

func main() {
	var (
		cfgPath = flag.String("config", "", "path to YAML config (optional)")
		addr    = flag.String("addr", ":8787", "listen address")
	)
	flag.Parse()
	if err := run(*cfgPath, *addr); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// uiSession is one long-lived conversation: a persistent agent (its transcript,
// plan and recalled lessons survive across turns) bound to a stable workspace
// directory. The same id names the episode, the workspace and the SSE "run".
type uiSession struct {
	id      string
	workdir string
	agent   *agent.Agent
}

// srv holds the shared, run-independent collaborators.
type srv struct {
	cfg      *config.Config
	logger   *slog.Logger
	store    *store.Store
	model    domain.LLM
	embedder llm.Embedder
	guard    *safety.Guard
	wsBase   string

	mu       sync.Mutex            // serialises turns (shared store + one turn at a time)
	sessions map[string]*uiSession // live conversations, keyed by session id

	setMu        sync.RWMutex  // guards set
	set          modelSettings // runtime model connection (UI-editable)
	settingsPath string        // where non-secret settings persist
}

func run(cfgPath, addr string) error {
	cfg := config.Default()
	if cfgPath != "" {
		c, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		cfg = c
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.Open(cfg.Agent.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.InterruptOrphans(context.Background()); err != nil {
		logger.Warn("store.interrupt_orphans", "err", err.Error())
	} else if n > 0 {
		logger.Info("store.interrupt_orphans", "fixed", n)
	}

	model, err := llm.New(cfg.LLM)
	if err != nil {
		return err
	}
	var embedder llm.Embedder
	if e, err := llm.NewEmbedder(cfg.Learn.EmbedModel, cfg.LLM.Timeout); err != nil {
		return err
	} else if e != nil {
		embedder = e
	}

	wsBase := cfg.Agent.Workspace
	if !filepath.IsAbs(wsBase) {
		cwd, _ := os.Getwd()
		wsBase = filepath.Join(cwd, wsBase)
	}
	if err := os.MkdirAll(wsBase, 0o755); err != nil {
		return err
	}

	s := &srv{
		cfg: cfg, logger: logger, store: st, model: model,
		embedder: embedder, guard: safety.NewGuard(cfg.Safety), wsBase: wsBase,
		sessions: map[string]*uiSession{},
	}
	s.initSettings()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/file", s.handleFile)
	mux.HandleFunc("/episodes", s.handleEpisodes)
	mux.HandleFunc("/episode", s.handleEpisode)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/models", s.handleModels)

	logger.Info("server.listen", "addr", addr, "workspace", wsBase, "db", cfg.Agent.DBPath)
	fmt.Fprintf(os.Stderr, "\n  live-agent web UI:  http://localhost%s\n\n", hostAddr(addr))
	return http.ListenAndServe(addr, mux)
}

func hostAddr(a string) string {
	if strings.HasPrefix(a, ":") {
		return a
	}
	if i := strings.LastIndex(a, ":"); i >= 0 {
		return a[i:]
	}
	return ":" + a
}

func (s *srv) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

// handleStream runs one conversational turn and streams events as SSE. A
// "session" query parameter continues an existing conversation (same workspace
// and context); when absent or unknown, a new session is created. A session is
// long-lived: it does NOT end when a task finishes or a question is answered.
func (s *srv) handleStream(w http.ResponseWriter, r *http.Request) {
	task := strings.TrimSpace(r.URL.Query().Get("task"))
	if task == "" {
		http.Error(w, "missing task", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// One turn at a time (shared store + serialised mutable session state).
	if !s.mu.TryLock() {
		send(w, flusher, map[string]any{"type": "busy", "text": "another turn is in progress; try again shortly"})
		send(w, flusher, map[string]any{"type": "done"})
		return
	}
	defer s.mu.Unlock()

	sess, err := s.resolveSession(r.Context(), sessionID, task, func(e agent.Event) {
		payload := map[string]any{}
		b, _ := json.Marshal(e)
		_ = json.Unmarshal(b, &payload)
		send(w, flusher, payload)
	})
	if err != nil {
		send(w, flusher, map[string]any{"type": "warn", "text": "cannot start session: " + err.Error(), "is_error": true})
		send(w, flusher, map[string]any{"type": "done"})
		return
	}

	// Honour the chosen model for this turn (empty = current default).
	llmModel, err := s.buildLLM(model)
	if err != nil {
		send(w, flusher, map[string]any{"type": "warn", "text": "model not available: " + err.Error(), "is_error": true})
		send(w, flusher, map[string]any{"type": "done"})
		return
	}
	sess.agent.SetLLM(llmModel)

	// Every emitted event is tagged with the session id, which also names the
	// workspace ("run") the UI reads files from.
	sess.agent.SetEmitter(func(e agent.Event) {
		payload := map[string]any{}
		b, _ := json.Marshal(e)
		_ = json.Unmarshal(b, &payload)
		payload["run"] = sess.id
		send(w, flusher, payload)
	})

	res := sess.agent.RunTurn(r.Context(), task)

	usedModel := model
	if usedModel == "" {
		s.setMu.RLock()
		usedModel = s.set.Model
		s.setMu.RUnlock()
	}
	send(w, flusher, map[string]any{
		"type": "result", "run": sess.id, "session": sess.id, "model": usedModel,
		"episode": res.EpisodeID, "steps": res.Steps,
		"finished": res.Finished, "success": res.Success, "verified": res.Verified,
		"reply": res.Reply, "stop_reason": res.StopReason, "summary": res.Summary,
		"files": listFiles(sess.workdir),
	})
	send(w, flusher, map[string]any{"type": "done"})
}

// resolveSession returns the live session for id, reviving it from the store if
// the server lost it (restart), or creating a fresh one. The provided emitter
// is bound only for the initial start/rehydrate events; handleStream rebinds it
// for the turn itself.
func (s *srv) resolveSession(ctx context.Context, id, task string, emit agent.Emitter) (*uiSession, error) {
	if id != "" {
		if sess, ok := s.sessions[id]; ok {
			return sess, nil
		}
	}

	// New or revived session: one id names episode + workspace + stream.
	newID := id
	if newID == "" {
		newID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	workdir := filepath.Join(s.wsBase, newID)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	ts := tool.NewToolset()
	(&tool.Builtins{Workdir: workdir, Timeout: s.cfg.Limits.StepTimeout}).RegisterAll(ts)
	ag := agent.New(agent.Deps{
		LLM: s.model, Embedder: s.embedder, Tools: ts, Store: s.store, Guard: s.guard,
		Config: s.cfg, Logger: s.logger, Workdir: workdir,
		PromptExtension: s.cfg.Agent.SystemPrompt, OnEvent: emit,
	})

	// If the id refers to a known past session, revive it from stored events so
	// the conversation continues with its history; otherwise start fresh.
	if id != "" {
		if meta, err := s.store.EpisodeMeta(ctx, id); err == nil {
			events, _ := s.store.EpisodeEvents(ctx, id)
			ag.Rehydrate(ctx, id, meta.Task, events)
			sess := &uiSession{id: id, workdir: workdir, agent: ag}
			s.sessions[id] = sess
			return sess, nil
		}
	}
	if _, err := ag.StartSession(ctx, newID, task); err != nil {
		return nil, err
	}
	sess := &uiSession{id: newID, workdir: workdir, agent: ag}
	s.sessions[newID] = sess
	return sess, nil
}

// handleEpisodes lists recent runs (sessions) for the sidebar.
func (s *srv) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	eps, err := s.store.ListEpisodes(r.Context(), s.cfg.Agent.Name, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"episodes": eps})
}

// handleEpisode returns one past run's meta + recorded events, for read-only
// replay in the chat when a sidebar session is selected.
func (s *srv) handleEpisode(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	meta, err := s.store.EpisodeMeta(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	events, _ := s.store.EpisodeEvents(r.Context(), id)
	writeJSON(w, map[string]any{"episode": meta, "events": events})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// handleFile serves a produced file's text content, constrained to the run's
// workspace so nothing outside can be read.
func (s *srv) handleFile(w http.ResponseWriter, r *http.Request) {
	runDir := filepath.Base(r.URL.Query().Get("run"))
	name := r.URL.Query().Get("name")
	if runDir == "" || name == "" {
		http.Error(w, "missing run/name", http.StatusBadRequest)
		return
	}
	base := filepath.Join(s.wsBase, runDir)
	full := filepath.Clean(filepath.Join(base, name))
	if !strings.HasPrefix(full, filepath.Clean(base)+string(os.PathSeparator)) && full != filepath.Clean(base) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	const max = 200_000
	if len(data) > max {
		data = append(data[:max], []byte("\n...[truncated]")...)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

type fileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir"`
}

func listFiles(dir string) []fileInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var sz int64
		if info, err := e.Info(); err == nil {
			sz = info.Size()
		}
		out = append(out, fileInfo{Name: e.Name(), Size: sz, Dir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func send(w http.ResponseWriter, f http.Flusher, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	f.Flush()
}
