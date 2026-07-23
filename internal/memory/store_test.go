package memory_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"liveagent/internal/domain"
	"liveagent/internal/memory"
)

func openTemp(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Criterion 7: a similar scenario retrieves an already-ACTIVE principle, ranked
// above an unrelated deprecated one.
func TestRetrieveActivePrincipleForMatchingTags(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	active := domain.Principle{
		ID: "p_active", AgentID: "a", Text: "Rest when energy is low.",
		ApplicableConditions: []string{"energy_low"}, Confidence: 0.9,
		Status: domain.StatusActive, Evidence: 3, SuccessRate: 0.8,
		Tags: []string{"energy_low"}, UpdatedAt: time.Now().UTC(),
	}
	unrelated := domain.Principle{
		ID: "p_dep", AgentID: "a", Text: "Something unrelated about combat.",
		Status: domain.StatusDeprecated, Confidence: 0.9, Tags: []string{"combat"},
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.SavePrinciple(ctx, active); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePrinciple(ctx, unrelated); err != nil {
		t.Fatal(err)
	}

	items, err := s.Retrieve(ctx, domain.MemoryQuery{
		Tags: []string{"energy_low"}, Keywords: []string{"energy"},
		Kinds: []string{domain.KindPrinciple}, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one retrieved principle")
	}
	top := items[0]
	if top.Principle == nil || top.Principle.ID != "p_active" {
		t.Fatalf("expected active energy_low principle ranked first, got %+v", top)
	}
}

// Criteria 1 & 2: state persists, and reopening the DB recovers the same
// AgentID, age and memory.
func TestPersistenceAndRecoveryAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent.db")
	ctx := context.Background()

	// First "process": create state, append experiences.
	s1, err := memory.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	st := domain.AgentState{
		AgentID: "agent_fixed", CreatedAt: time.Now().UTC(), Age: 7,
		WorldState: map[string]any{}, BodyState: map[string]any{"energy": 50.0},
	}
	if err := s1.SaveAgentState(ctx, "explorer", st); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_ = s1.AppendExperience(ctx, domain.Experience{AgentID: "agent_fixed", Tick: int64(i)})
	}
	s1.Close()

	// Second "process": reopen, recover.
	s2, err := memory.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, ok, err := s2.LoadAgentByName(ctx, "explorer")
	if err != nil || !ok {
		t.Fatalf("expected to recover agent, ok=%v err=%v", ok, err)
	}
	if got.AgentID != "agent_fixed" {
		t.Fatalf("expected same AgentID, got %q", got.AgentID)
	}
	if got.Age != 7 {
		t.Fatalf("expected age 7 recovered, got %d", got.Age)
	}
	n, _ := s2.CountExperiences(ctx, "agent_fixed")
	if n != 3 {
		t.Fatalf("expected 3 experiences recovered, got %d", n)
	}
}
