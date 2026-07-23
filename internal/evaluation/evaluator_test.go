package evaluation

import (
	"context"
	"testing"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

func regWithActions(names ...string) *domain.ActionRegistry {
	r := domain.NewActionRegistry()
	for _, n := range names {
		r.Register(domain.ActionSchema{Name: n, Parameters: map[string]domain.ParamSpec{}})
	}
	return r
}

// Criterion 8: a skill may only reference registered actions — never arbitrary code.
func TestEvaluateSkillRejectsUnregisteredAction(t *testing.T) {
	e := New(config.EvolutionConfig{MinConfidence: 0}, regWithActions("rest", "collect"))
	sk := domain.Skill{Name: "evil", Confidence: 1, Steps: []domain.SkillStep{
		{Action: "rest"},
		{Action: "exec_shell"}, // not registered
	}}
	if res := e.EvaluateSkill(context.Background(), sk); res.Passed {
		t.Fatal("skill referencing unregistered action must not pass evaluation")
	}
}

func TestEvaluateSkillAcceptsRegisteredActions(t *testing.T) {
	e := New(config.EvolutionConfig{MinConfidence: 0}, regWithActions("rest", "collect"))
	sk := domain.Skill{Name: "recover", Confidence: 0.9, Steps: []domain.SkillStep{
		{Action: "rest"}, {Action: "collect"},
	}}
	if res := e.EvaluateSkill(context.Background(), sk); !res.Passed {
		t.Fatalf("valid skill should pass: %s", res.Reason)
	}
}

// Criterion 6 (part): a principle with insufficient evidence cannot pass.
func TestEvaluatePrincipleNeedsEvidence(t *testing.T) {
	e := New(config.EvolutionConfig{MinEvidence: 2, MinConfidence: 0.6}, regWithActions())
	p := domain.Principle{Text: "rest when low", Confidence: 0.9, Evidence: 1}
	if res := e.EvaluatePrinciple(context.Background(), p); res.Passed {
		t.Fatal("principle with evidence=1 must not pass when min_evidence=2")
	}
	p.Evidence = 2
	if res := e.EvaluatePrinciple(context.Background(), p); !res.Passed {
		t.Fatalf("principle with sufficient evidence/confidence should pass: %s", res.Reason)
	}
}
