package kernel_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"liveagent/examples/gridworld"
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
	"liveagent/internal/skills"
)

func testConfig(db string, maxTicks int64) *config.Config {
	return &config.Config{
		Agent:   config.AgentConfig{Name: "test-explorer", DBPath: db},
		Runtime: config.RuntimeConfig{MaxTicks: maxTicks, TickTimeout: 5 * time.Second},
		LLM:     config.LLMConfig{Provider: "fake", MaxRetries: 1, Timeout: time.Second},
		Reward: config.RewardConfig{Weights: config.RewardWeights{
			TaskSuccess: 3, Homeostasis: 1, InformationGain: 0.5, ResourceCost: 0.5, RiskCost: 2,
		}},
		Evolution: config.EvolutionConfig{MinEvidence: 2, MinConfidence: 0.6},
		Goals: []config.GoalConfig{
			{ID: "explore_world", Description: "explore", Priority: 1, Tags: []string{"explore", "exploration_task"}},
			{ID: "stay_alive", Description: "recover", Priority: 0.8, Tags: []string{"recover", "survive", "energy_low"}},
		},
		Body: config.BodyConfig{Fields: []config.BodyField{
			{Name: "energy", Initial: 100, Max: 100}, {Name: "health", Initial: 100, Max: 100},
		}},
		Environment: config.EnvironmentConfig{Type: "gridworld", Params: map[string]any{
			"width": 5, "height": 5, "episode_length": 6, "explore_goal": 0.6,
		}},
	}
}

// buildRuntime wires the full stack exactly like cmd/agent, but for a test.
func buildRuntime(t *testing.T, cfg *config.Config, store *memory.Store) (*kernel.Runtime, *domain.AgentState) {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	model, err := llm.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	env := gridworld.New(gridworld.Params{Width: 5, Height: 5, EpisodeLength: 6, ExploreGoal: 0.6, Seed: 1})
	reg := domain.NewActionRegistry()
	reg.RegisterAll(env.AvailableActions(ctx))
	body := embodiment.New(cfg.Body, env, nil)

	st, ok, err := store.LoadAgentByName(ctx, cfg.Agent.Name)
	var state *domain.AgentState
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		state = &st
	} else {
		state = &domain.AgentState{
			AgentID: domain.NewID("agent"), CreatedAt: time.Now().UTC(),
			WorldState: map[string]any{}, BodyState: map[string]any{},
			SelfModel: domain.SelfModel{Capabilities: map[string]float64{}, Beliefs: map[string]any{}, RiskEstimates: map[string]float64{}},
		}
		if err := store.SaveAgentState(ctx, cfg.Agent.Name, *state); err != nil {
			t.Fatal(err)
		}
	}

	guard := safety.NewGuard(cfg.Safety)
	evaluator := evaluation.New(cfg.Evolution, reg)
	evo := evolution.New(store, evaluator, cfg.Evolution)
	agent := cognition.New(cognition.Deps{
		State: state, LLM: model, Memory: store, Registry: reg,
		Guard: guard, Expander: skills.NewExpander(reg), Goals: cfg.Goals,
	})
	rt := kernel.New(kernel.Deps{
		Config: cfg, Store: store, Env: env, Body: body, Agent: agent, State: state,
		Guard: guard, Reflector: reflection.New(model, 512), Evolution: evo, Logger: logger,
	})
	return rt, state
}

// Criteria 1, 2, 10, 11: the loop runs offline, persists state, and a restart
// recovers the same identity/age and accumulated memory.
func TestRunPersistAndRecover(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "agent.db")
	ctx := context.Background()

	// First run: 8 ticks.
	cfg := testConfig(db, 8)
	s1, err := memory.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	rt1, st1 := buildRuntime(t, cfg, s1)
	if err := rt1.Run(ctx); err != nil {
		t.Fatalf("run1: %v", err)
	}
	firstID := st1.AgentID
	if st1.Age < 8 {
		t.Fatalf("expected age >= 8 after run, got %d", st1.Age)
	}
	nExp, _ := s1.CountExperiences(ctx, firstID)
	if nExp == 0 {
		t.Fatal("expected experiences to be persisted")
	}
	s1.Close()

	// Second run: reopen -> must recover the SAME agent, then advance further.
	cfg2 := testConfig(db, 16)
	s2, err := memory.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rt2, st2 := buildRuntime(t, cfg2, s2)
	if st2.AgentID != firstID {
		t.Fatalf("restart did not recover identity: %q != %q", st2.AgentID, firstID)
	}
	if st2.Age < 8 {
		t.Fatalf("restart did not recover age, got %d", st2.Age)
	}
	if err := rt2.Run(ctx); err != nil {
		t.Fatalf("run2: %v", err)
	}
	if st2.Age < 16 {
		t.Fatalf("expected age to advance to >=16, got %d", st2.Age)
	}
	nExp2, _ := s2.CountExperiences(ctx, firstID)
	if nExp2 <= nExp {
		t.Fatalf("expected more experiences after second run: %d <= %d", nExp2, nExp)
	}
}

// Criterion 5 (integration): running long enough produces at least one candidate
// principle from failed episodes.
func TestLoopProducesPrinciples(t *testing.T) {
	db := filepath.Join(t.TempDir(), "agent.db")
	cfg := testConfig(db, 30)
	store, err := memory.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rt, _ := buildRuntime(t, cfg, store)
	if err := rt.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ps, _ := store.ListPrinciples(context.Background(), "")
	if len(ps) == 0 {
		t.Fatal("expected at least one principle after 30 ticks")
	}
}
