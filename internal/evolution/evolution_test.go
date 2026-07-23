package evolution_test

import (
	"context"
	"path/filepath"
	"testing"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/evaluation"
	"liveagent/internal/evolution"
	"liveagent/internal/memory"
	"liveagent/internal/reflection"
)

func setup(t *testing.T) (*memory.Store, *evolution.Manager, config.EvolutionConfig) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "test.db")
	store, err := memory.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	reg := domain.NewActionRegistry()
	reg.Register(domain.ActionSchema{Name: "rest", Parameters: map[string]domain.ParamSpec{}})
	reg.Register(domain.ActionSchema{Name: "collect", Parameters: map[string]domain.ParamSpec{}})

	cfg := config.EvolutionConfig{MinEvidence: 2, MinConfidence: 0.6, MinSuccessRate: 0}
	ev := evaluation.New(cfg, reg)
	return store, evolution.New(store, ev, cfg), cfg
}

func failureReflection() reflection.Reflection {
	return reflection.Reflection{
		Parsed:               true,
		Success:              false,
		Cause:                "energy depleted",
		Principle:            "Rest when energy is low before continuing.",
		ApplicableConditions: []string{"energy_low"},
		Confidence:           0.8,
		SuggestedSkill: &reflection.SuggestedSkill{
			Name:  "recover_energy",
			Steps: []domain.SkillStep{{Action: "rest"}, {Action: "collect"}},
		},
	}
}

// Criterion 5: a failed episode produces a CANDIDATE principle (not active yet).
func TestFailedEpisodeProducesCandidatePrinciple(t *testing.T) {
	store, mgr, _ := setup(t)
	ctx := context.Background()

	if _, err := mgr.ProcessReflection(ctx, "agent1", failureReflection(), false); err != nil {
		t.Fatalf("process reflection: %v", err)
	}
	ps, _ := store.ListPrinciples(ctx, "")
	if len(ps) != 1 {
		t.Fatalf("expected 1 principle, got %d", len(ps))
	}
	if ps[0].Status != domain.StatusCandidate {
		t.Fatalf("expected candidate after one failure, got %q", ps[0].Status)
	}
	if ps[0].Evidence != 1 {
		t.Fatalf("expected evidence=1, got %d", ps[0].Evidence)
	}
}

// Criterion 6: a principle only activates AFTER passing evaluation (enough
// evidence), and duplicates are merged rather than appended.
func TestPrincipleActivatesOnlyAfterEnoughEvidence(t *testing.T) {
	store, mgr, _ := setup(t)
	ctx := context.Background()

	_, _ = mgr.ProcessReflection(ctx, "agent1", failureReflection(), false)
	ps, _ := store.ListPrinciples(ctx, "")
	if ps[0].Status == domain.StatusActive {
		t.Fatal("principle must not activate from a single episode")
	}

	// Second corroborating experience: evidence reaches MinEvidence -> active.
	_, _ = mgr.ProcessReflection(ctx, "agent1", failureReflection(), false)
	ps, _ = store.ListPrinciples(ctx, "")
	if len(ps) != 1 {
		t.Fatalf("duplicate principle should be merged, got %d rows", len(ps))
	}
	if ps[0].Status != domain.StatusActive {
		t.Fatalf("expected active after enough evidence, got %q", ps[0].Status)
	}
	if ps[0].Evidence != 2 {
		t.Fatalf("expected merged evidence=2, got %d", ps[0].Evidence)
	}
}

// A skill suggested by reflection activates only if its steps are all registered.
func TestSuggestedSkillActivatesWhenActionsRegistered(t *testing.T) {
	store, mgr, _ := setup(t)
	ctx := context.Background()
	_, _ = mgr.ProcessReflection(ctx, "agent1", failureReflection(), false)

	sks, _ := store.ListSkills(ctx, "")
	if len(sks) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(sks))
	}
	if sks[0].Status != domain.StatusActive {
		t.Fatalf("expected skill active (all actions registered), got %q", sks[0].Status)
	}
}

// An unparsed reflection must NOT create any principle.
func TestUnparsedReflectionCreatesNothing(t *testing.T) {
	store, mgr, _ := setup(t)
	ctx := context.Background()
	_, _ = mgr.ProcessReflection(ctx, "agent1", reflection.Reflection{Parsed: false, Raw: "garbage"}, false)
	ps, _ := store.ListPrinciples(ctx, "")
	if len(ps) != 0 {
		t.Fatalf("unparsed reflection must not create principles, got %d", len(ps))
	}
}
