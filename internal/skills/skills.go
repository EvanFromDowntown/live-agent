// Package skills expands validated Skill DSL into concrete action sequences. A
// skill is never "code": it is a list of steps, each naming a registered action.
// Expansion re-validates every step against the registry as defence in depth.
package skills

import (
	"fmt"

	"liveagent/internal/domain"
)

// Expander turns skills into executable plans, validating against the registry.
type Expander struct {
	registry *domain.ActionRegistry
}

// NewExpander builds an Expander.
func NewExpander(registry *domain.ActionRegistry) *Expander {
	return &Expander{registry: registry}
}

// Expand converts a skill into a Plan of validated actions. It returns an error
// if any step references an unregistered action or has invalid parameters —
// guaranteeing skills can only ever invoke pre-registered actions.
func (e *Expander) Expand(goalID string, sk domain.Skill) (domain.Plan, error) {
	steps := make([]domain.Action, 0, len(sk.Steps))
	for i, s := range sk.Steps {
		act := domain.Action{Name: s.Action, Parameters: s.Parameters}
		if act.Parameters == nil {
			act.Parameters = map[string]any{}
		}
		if err := e.registry.Validate(act); err != nil {
			return domain.Plan{}, fmt.Errorf("skill %q step %d: %w", sk.Name, i, err)
		}
		steps = append(steps, act)
	}
	return domain.Plan{
		GoalID:    goalID,
		Steps:     steps,
		Rationale: "expanded from skill " + sk.Name,
		SkillName: sk.Name,
	}, nil
}
