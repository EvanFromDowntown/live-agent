// Command eval is an offline A/B harness for the verified learning loop. It runs
// the same batch of tasks twice — arm A WITH lesson recall, arm B WITHOUT — and
// reports the difference in verified-success rate, steps and token cost. Both
// arms run with learning (distill/credit/evict) DISABLED so the shared note
// store is not mutated during the experiment; only recall is varied. This
// isolates the effect of injecting past lessons into new tasks.
//
// Usage:
//
//	go run ./cmd/eval -tasks tasks.json           # tasks.json: [{"name":..,"task":..}]
//	go run ./cmd/eval                              # uses a small built-in task set
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"liveagent/internal/agent"
	"liveagent/internal/config"
	"liveagent/internal/llm"
	"liveagent/internal/safety"
	"liveagent/internal/store"
	"liveagent/internal/tool"
)

type evalTask struct {
	Name string `json:"name"`
	Task string `json:"task"`
}

// builtinTasks is a small, self-contained default suite (no network needed) so
// the harness runs out of the box. Provide -tasks for your own suite.
var builtinTasks = []evalTask{
	{Name: "fizzbuzz", Task: "Write a Python program fizzbuzz.py that prints FizzBuzz for numbers 1..30, run it, and confirm the output is correct."},
	{Name: "wordcount", Task: "Create data.txt containing the sentence 'the quick brown fox jumps over the lazy dog the fox', then write and run a script that counts word frequencies and writes them (word,count per line, sorted by count desc) to counts.csv."},
	{Name: "json-transform", Task: "Create input.json with an array of 5 objects each having name and age. Write a program that outputs output.json containing only the names of people older than 30, then verify output.json is valid JSON."},
	{Name: "primes", Task: "Write and run a program that computes the first 20 prime numbers and writes them space-separated to primes.txt. Verify the file contains exactly 20 primes."},
}

type arm struct {
	name     string
	runs     int
	verified int
	success  int
	steps    int
	tokens   int
	promptTk int
	complTk  int
	elapsed  int64
	llmCalls int
}

func (a *arm) add(r agent.RunResult) {
	a.runs++
	if r.Verified {
		a.verified++
	}
	if r.Success {
		a.success++
	}
	a.steps += r.Steps
	a.tokens += r.TotalTokens
	a.promptTk += r.PromptTokens
	a.complTk += r.CompletionTokens
	a.elapsed += r.ElapsedMS
	a.llmCalls += r.LLMCalls
}

func main() {
	var (
		cfgPath   = flag.String("config", "", "path to YAML config (optional)")
		tasksPath = flag.String("tasks", "", "path to a JSON task suite [{name,task}]; empty = built-in set")
		repeat    = flag.Int("repeat", 1, "how many times to run each task per arm")
	)
	flag.Parse()
	if err := run(*cfgPath, *tasksPath, *repeat); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func loadTasks(path string) ([]evalTask, error) {
	if strings.TrimSpace(path) == "" {
		return builtinTasks, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tasks []evalTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks file: %w", err)
	}
	for i := range tasks {
		if strings.TrimSpace(tasks[i].Name) == "" {
			tasks[i].Name = fmt.Sprintf("task-%d", i+1)
		}
	}
	return tasks, nil
}

func run(cfgPath, tasksPath string, repeat int) error {
	cfg := config.Default()
	if cfgPath != "" {
		c, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		cfg = c
	}
	if repeat < 1 {
		repeat = 1
	}
	tasks, err := loadTasks(tasksPath)
	if err != nil {
		return err
	}
	// Quiet logger: the harness prints its own report.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := store.Open(cfg.Agent.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	model, err := llm.New(cfg.LLM)
	if err != nil {
		return err
	}
	var embedder llm.Embedder
	if e, _ := llm.NewEmbedder(cfg.Learn.EmbedModel, cfg.LLM.Timeout); e != nil {
		embedder = e
	}
	guard := safety.NewGuard(cfg.Safety)

	base, err := os.MkdirTemp("", "liveagent-eval-*")
	if err != nil {
		return err
	}
	fmt.Printf("eval: %d task(s) x %d repeat x 2 arms  (workspaces under %s)\n\n", len(tasks), repeat, base)

	withArm := &arm{name: "A: recall ON "}
	withoutArm := &arm{name: "B: recall OFF"}

	runOne := func(t evalTask, recallDisabled bool, tag string) agent.RunResult {
		wd := filepath.Join(base, sanitize(t.Name)+"-"+tag)
		_ = os.MkdirAll(wd, 0o755)
		ts := tool.NewToolset()
		(&tool.Builtins{Workdir: wd, Timeout: cfg.Limits.StepTimeout}).RegisterAll(ts)
		ag := agent.New(agent.Deps{
			LLM: model, Embedder: embedder, Tools: ts, Store: st, Guard: guard,
			Config: cfg, Logger: logger, Workdir: wd,
			PromptExtension: cfg.Agent.SystemPrompt,
			RecallDisabled:  recallDisabled,
			LearnDisabled:   true, // never mutate notes during the experiment
		})
		res, err := ag.Run(context.Background(), t.Task)
		if err != nil {
			fmt.Printf("  [%s] %s: ERROR %v\n", tag, t.Name, err)
		}
		return res
	}

	for _, t := range tasks {
		for i := 0; i < repeat; i++ {
			ra := runOne(t, false, fmt.Sprintf("A%d", i))
			withArm.add(ra)
			rb := runOne(t, true, fmt.Sprintf("B%d", i))
			withoutArm.add(rb)
			fmt.Printf("  %-16s  A[v=%v steps=%2d tok=%5d]  B[v=%v steps=%2d tok=%5d]\n",
				t.Name, ra.Verified, ra.Steps, ra.TotalTokens, rb.Verified, rb.Steps, rb.TotalTokens)
		}
	}

	fmt.Println()
	report(withArm)
	report(withoutArm)
	fmt.Println()
	fmt.Printf("Δ verified-success (A−B): %+.0f%%   Δ avg steps: %+.1f   Δ avg tokens: %+.0f\n",
		rate(withArm.verified, withArm.runs)-rate(withoutArm.verified, withoutArm.runs),
		avg(withArm.steps, withArm.runs)-avg(withoutArm.steps, withoutArm.runs),
		avg(withArm.tokens, withArm.runs)-avg(withoutArm.tokens, withoutArm.runs))
	fmt.Printf("total wall time: %s\n", time.Duration((withArm.elapsed+withoutArm.elapsed))*time.Millisecond)
	return nil
}

func report(a *arm) {
	if a.runs == 0 {
		fmt.Printf("%s  (no runs)\n", a.name)
		return
	}
	fmt.Printf("%s  runs=%d  verified=%d (%.0f%%)  success=%d (%.0f%%)  avg_steps=%.1f  avg_tokens=%.0f  avg_calls=%.1f  avg_ms=%.0f\n",
		a.name, a.runs, a.verified, rate(a.verified, a.runs), a.success, rate(a.success, a.runs),
		avg(a.steps, a.runs), avg(a.tokens, a.runs), avg(a.llmCalls, a.runs), avg(int(a.elapsed), a.runs))
}

func rate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}
func avg(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "task"
	}
	return b.String()
}
