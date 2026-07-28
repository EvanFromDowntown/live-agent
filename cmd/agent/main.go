// Command agent is the entry point for the general agent. It wires
// configuration, the LLM provider, the SQLite store, the host toolset and the
// safety guard into the reactive tool-use loop, reads a task from stdin (or the
// -task flag), and runs it to completion.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"liveagent/internal/agent"
	"liveagent/internal/config"
	"liveagent/internal/llm"
	"liveagent/internal/safety"
	"liveagent/internal/store"
	"liveagent/internal/tool"
)

func main() {
	var (
		cfgPath  = flag.String("config", "", "path to YAML config (optional; defaults used if empty)")
		taskFlag = flag.String("task", "", "task text (if empty, read from stdin)")
	)
	flag.Parse()

	if err := run(*cfgPath, *taskFlag); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(cfgPath, taskFlag string) error {
	var (
		cfg *config.Config
		err error
	)
	if cfgPath != "" {
		if cfg, err = config.Load(cfgPath); err != nil {
			return err
		}
	} else {
		cfg = config.Default()
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Persistence.
	st, err := store.Open(cfg.Agent.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	// LLM cognitive module.
	model, err := llm.New(cfg.LLM)
	if err != nil {
		return err
	}

	// Optional embedder for semantic lesson recall (nil => lexical fallback).
	// NewEmbedder returns a typed nil when unconfigured; keep the interface nil so
	// the agent's `embedder != nil` check is accurate.
	var embedder llm.Embedder
	if e, err := llm.NewEmbedder(cfg.Learn.EmbedModel, cfg.LLM.Timeout); err != nil {
		return err
	} else if e != nil {
		embedder = e
		logger.Info("embeddings.enabled", "model", e.Model())
	}

	// Working directory for host actions.
	workdir := cfg.Agent.Workspace
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return fmt.Errorf("cannot create workspace %q: %w", workdir, err)
	}
	absWork, _ := os.Getwd()
	if !strings.HasPrefix(workdir, "/") {
		workdir = absWork + "/" + workdir
	}

	// Toolset: host built-ins.
	ts := tool.NewToolset()
	(&tool.Builtins{Workdir: workdir, Timeout: cfg.Limits.StepTimeout}).RegisterAll(ts)

	guard := safety.NewGuard(cfg.Safety)

	ag := agent.New(agent.Deps{
		LLM: model, Embedder: embedder, Tools: ts, Store: st, Guard: guard,
		Config: cfg, Logger: logger, Workdir: workdir,
		PromptExtension: cfg.Agent.SystemPrompt,
	})

	// Task from flag or stdin.
	task := strings.TrimSpace(taskFlag)
	if task == "" {
		fmt.Fprintln(os.Stderr, "Enter the task, then press Ctrl-D:")
		data, _ := io.ReadAll(bufio.NewReader(os.Stdin))
		task = strings.TrimSpace(string(data))
	}
	if task == "" {
		return fmt.Errorf("no task provided (use -task or pipe it to stdin)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := ag.Run(ctx, task)
	if err != nil {
		return err
	}

	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("episode:     %s\n", res.EpisodeID)
	fmt.Printf("steps:       %d\n", res.Steps)
	fmt.Printf("stop reason: %s\n", res.StopReason)
	fmt.Printf("finished:    %v (success=%v, verified=%v)\n", res.Finished, res.Success, res.Verified)
	fmt.Printf("summary:     %s\n", res.Summary)
	fmt.Printf("workspace:   %s\n", workdir)
	return nil
}
