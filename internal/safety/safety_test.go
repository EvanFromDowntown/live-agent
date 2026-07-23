package safety

import (
	"testing"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// Criterion 4: an action that violates a hard constraint must be rejected even
// when its expected reward is very high.
func TestForbiddenActionRejectedRegardlessOfReward(t *testing.T) {
	g := NewGuard(config.SafetyConfig{Constraints: []config.ConstraintConfig{
		{Name: "no-self-destruct", Type: "forbidden_action", Action: "self_destruct", Reason: "never allowed"},
	}})

	// The reward for this action would be enormous, but safety must still win.
	act := domain.Action{Name: "self_destruct"}
	dec := g.Check(act, map[string]any{"energy": 100.0})
	if dec.Allowed {
		t.Fatal("forbidden action was allowed despite hard constraint")
	}
	if dec.Constraint != "no-self-destruct" {
		t.Fatalf("unexpected constraint name %q", dec.Constraint)
	}
}

func TestBodyMinConstraint(t *testing.T) {
	g := NewGuard(config.SafetyConfig{Constraints: []config.ConstraintConfig{
		{Name: "no-strenuous-when-exhausted", Type: "body_min", Field: "energy", Value: 10, AppliesTo: "sprint"},
	}})

	if dec := g.Check(domain.Action{Name: "sprint"}, map[string]any{"energy": 5.0}); dec.Allowed {
		t.Fatal("expected sprint to be rejected when energy below minimum")
	}
	if dec := g.Check(domain.Action{Name: "sprint"}, map[string]any{"energy": 50.0}); !dec.Allowed {
		t.Fatal("expected sprint to be allowed when energy sufficient")
	}
	// A different action is unaffected by an action-scoped constraint.
	if dec := g.Check(domain.Action{Name: "rest"}, map[string]any{"energy": 5.0}); !dec.Allowed {
		t.Fatal("expected rest to be allowed")
	}
}
