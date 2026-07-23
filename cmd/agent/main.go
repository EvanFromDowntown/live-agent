// Command agent is the entry point. It wires configuration, the LLM provider,
// SQLite memory, the environment/embodiment, cognition, safety, reflection and
// evolution into the resident kernel, then runs the loop with graceful shutdown.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"liveagent/examples/gridworld"
	"liveagent/examples/webagent"
	"liveagent/internal/cognition"
	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/embodiment"
	"liveagent/internal/evaluation"
	"liveagent/internal/evolution"
	"liveagent/internal/kernel"
	"liveagent/internal/llm"
	"liveagent/internal/memory"
	"liveagent/internal/reflection"
	"liveagent/internal/safety"
	"liveagent/internal/sandbox"
	"liveagent/internal/skills"
)

func main() {
	var (
		cfgPath  = flag.String("config", "configs/gridworld.yaml", "path to YAML config")
		step     = flag.Bool("step", false, "run a single tick and exit")
		maxTicks = flag.Int64("max-ticks", -1, "override runtime.max_ticks (-1 = use config)")
	)
	flag.Parse()

	if err := run(*cfgPath, *step, *maxTicks); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(cfgPath string, step bool, maxTicks int64) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if step {
		cfg.Runtime.StepMode = true
	}
	if maxTicks >= 0 {
		cfg.Runtime.MaxTicks = maxTicks
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Memory / persistence.
	store, err := memory.Open(cfg.Agent.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	// LLM cognitive module (fake or OpenAI-compatible).
	model, err := llm.New(cfg.LLM)
	if err != nil {
		return err
	}

	// Environment (GridWorld example).
	env, err := buildEnvironment(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Action registry = whitelist derived from the environment's actions.
	registry := domain.NewActionRegistry()
	registry.RegisterAll(env.AvailableActions(ctx))

	// Embodiment: schema-driven body adapter reading from the environment.
	bodySource, ok := env.(embodiment.BodySource)
	if !ok {
		return fmt.Errorf("environment does not provide a body source")
	}
	body := embodiment.New(cfg.Body, bodySource, []domain.Capability{
		{Name: "locomotion", Description: "can move on the grid"},
		{Name: "perception", Description: "can observe surroundings"},
	})

	// Recover or create the agent's persistent identity/state.
	state, created, err := loadOrCreateState(ctx, store, cfg)
	if err != nil {
		return err
	}
	logger.Info("identity", "agent_id", state.AgentID, "age", state.Age, "new", created)

	// Safety, skills, evaluation, evolution, reflection.
	guard := safety.NewGuard(cfg.Safety)
	expander := skills.NewExpander(registry)
	evaluator := evaluation.New(cfg.Evolution, registry)
	evo := evolution.New(store, evaluator, cfg.Evolution)
	reflector := reflection.New(model, cfg.LLM.MaxTokens)

	// Cognition (the Agent).
	agent := cognition.New(cognition.Deps{
		State: state, LLM: model, Memory: store, Registry: registry,
		Guard: guard, Expander: expander, Goals: cfg.Goals,
		Temperature: cfg.LLM.Temperature, MaxTokens: cfg.LLM.MaxTokens,
	})

	rt := kernel.New(kernel.Deps{
		Config: cfg, Store: store, Env: env, Body: body, Agent: agent,
		State: state, Guard: guard, Reflector: reflector, Evolution: evo, Logger: logger,
	})

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return rt.Run(ctx)
}

// loadOrCreateState recovers the persisted agent (identity, age, self model,
// resource usage) or creates a fresh one. Body/world state are refreshed live.
func loadOrCreateState(ctx context.Context, store *memory.Store, cfg *config.Config) (*domain.AgentState, bool, error) {
	if st, ok, err := store.LoadAgentByName(ctx, cfg.Agent.Name); err != nil {
		return nil, false, err
	} else if ok {
		if st.WorldState == nil {
			st.WorldState = map[string]any{}
		}
		return &st, false, nil
	}

	st := &domain.AgentState{
		AgentID:      domain.NewID("agent"),
		CreatedAt:    time.Now().UTC(),
		Age:          0,
		WorldState:   map[string]any{},
		BodyState:    map[string]any{},
		SelfModel:    domain.SelfModel{Capabilities: map[string]float64{}, Beliefs: map[string]any{}, RiskEstimates: map[string]float64{}},
		ActivePolicy: "policy-v1",
	}
	if err := store.SaveAgentState(ctx, cfg.Agent.Name, *st); err != nil {
		return nil, false, err
	}
	_ = store.RecordPolicyVersion(ctx, st.AgentID, "policy-v1", "initial human-defined policy", true)
	return st, true, nil
}

// buildEnvironment constructs the configured environment. Only known, registered
// environment types are allowed.
func buildEnvironment(cfg *config.Config) (domain.Environment, error) {
	switch cfg.Environment.Type {
	case "webagent":
		box, err := sandbox.NewDocker(sandbox.Config{
			Image:          pStr(cfg, "image"),
			WorkDirHost:    pStr(cfg, "workspace"),
			Network:        pStr(cfg, "network"),
			MemoryMB:       pInt(cfg, "memory_mb"),
			CPUs:           pFloat(cfg, "cpus"),
			DefaultTimeout: pInt(cfg, "timeout_sec"),
		})
		if err != nil {
			return nil, err
		}
		return webagent.New(box, webagent.Params{
			Task:          pStr(cfg, "task"),
			StartURL:      pStr(cfg, "start_url"),
			SuccessFile:   pStr(cfg, "success_file"),
			SuccessMin:    pInt(cfg, "success_min"),
			SuccessFields: pStrSlice(cfg, "success_fields"),
			MinFieldChars: pInt(cfg, "min_field_chars"),
			MaxSteps:      int64(pInt(cfg, "max_steps")),
			Budget:        pFloat(cfg, "budget"),
		}), nil
	case "gridworld", "":
		return gridworld.New(gridworld.Params{
			Width:         pInt(cfg, "width"),
			Height:        pInt(cfg, "height"),
			NumResources:  pInt(cfg, "num_resources"),
			NumObstacles:  pInt(cfg, "num_obstacles"),
			Seed:          int64(pInt(cfg, "seed")),
			InitEnergy:    pFloat(cfg, "init_energy"),
			MaxEnergy:     pFloat(cfg, "max_energy"),
			InitHealth:    pFloat(cfg, "init_health"),
			MaxHealth:     pFloat(cfg, "max_health"),
			MaxLifespan:   int64(pInt(cfg, "max_lifespan")),
			MoveCost:      pFloat(cfg, "move_cost"),
			ExploreCost:   pFloat(cfg, "explore_cost"),
			ObserveCost:   pFloat(cfg, "observe_cost"),
			RestGain:      pFloat(cfg, "rest_gain"),
			CollectGain:   pFloat(cfg, "collect_gain"),
			EpisodeLength: int64(pInt(cfg, "episode_length")),
			ExploreGoal:   pFloat(cfg, "explore_goal"),
		}), nil
	default:
		return nil, fmt.Errorf("unknown environment type %q", cfg.Environment.Type)
	}
}

func pInt(cfg *config.Config, key string) int {
	switch v := cfg.Environment.Params[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func pStr(cfg *config.Config, key string) string {
	s, _ := cfg.Environment.Params[key].(string)
	return s
}

func pStrSlice(cfg *config.Config, key string) []string {
	raw, ok := cfg.Environment.Params[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func pFloat(cfg *config.Config, key string) float64 {
	switch v := cfg.Environment.Params[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}
