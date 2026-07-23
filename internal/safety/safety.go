// Package safety enforces human-defined hard constraints. A hard constraint
// ALWAYS overrides reward: an action that violates any constraint is rejected no
// matter how high its expected reward. Constraints are declarative (from config)
// and evaluated by built-in checkers only — never by executing arbitrary code.
package safety

import (
	"fmt"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// Decision is the result of a safety check.
type Decision struct {
	Allowed    bool
	Constraint string
	Reason     string
}

// Guard evaluates actions against configured hard constraints.
type Guard struct {
	constraints []config.ConstraintConfig
}

// NewGuard builds a Guard from config.
func NewGuard(cfg config.SafetyConfig) *Guard {
	return &Guard{constraints: cfg.Constraints}
}

// Check returns a Decision for the given action under the current body state.
// It is intentionally side-effect free so it can be called during planning and
// again immediately before execution (defence in depth).
func (g *Guard) Check(action domain.Action, bodyState map[string]any) Decision {
	for _, c := range g.constraints {
		switch c.Type {
		case "forbidden_action":
			if c.Action == action.Name {
				return deny(c, fmt.Sprintf("action %q is forbidden", action.Name))
			}
		case "body_min":
			if c.AppliesTo != "" && c.AppliesTo != action.Name {
				continue
			}
			if v, ok := bodyFloat(bodyState, c.Field); ok && v < c.Value {
				return deny(c, fmt.Sprintf("body.%s=%.2f is below hard minimum %.2f", c.Field, v, c.Value))
			}
		case "body_max":
			if c.AppliesTo != "" && c.AppliesTo != action.Name {
				continue
			}
			if v, ok := bodyFloat(bodyState, c.Field); ok && v > c.Value {
				return deny(c, fmt.Sprintf("body.%s=%.2f is above hard maximum %.2f", c.Field, v, c.Value))
			}
		}
	}
	return Decision{Allowed: true}
}

func deny(c config.ConstraintConfig, fallback string) Decision {
	reason := c.Reason
	if reason == "" {
		reason = fallback
	}
	return Decision{Allowed: false, Constraint: c.Name, Reason: reason}
}

func bodyFloat(body map[string]any, field string) (float64, bool) {
	if body == nil {
		return 0, false
	}
	switch n := body[field].(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
