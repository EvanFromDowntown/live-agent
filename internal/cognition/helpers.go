package cognition

import (
	"context"
	"encoding/json"

	"liveagent/internal/domain"
	"liveagent/internal/llm"
)

// situationTags derives retrieval tags from the current body/world state so the
// agent recalls knowledge relevant to "now" (e.g. energy_low).
func (a *Agent) situationTags() []string {
	tags := []string{}
	if e, ok := bodyFloat(a.state.BodyState, "energy"); ok && e < 30 {
		tags = append(tags, "energy_low")
	}
	if a.state.CurrentGoal != nil {
		tags = append(tags, a.state.CurrentGoal.Tags...)
	}
	tags = append(tags, "exploration_task", "long_horizon_task")
	return tags
}

// matchTags returns only the "sharp" situational tags used for SKILL matching,
// excluding the always-on generic tags so a recovery skill is not triggered
// during ordinary exploration.
func (a *Agent) matchTags() []string {
	tags := []string{}
	if e, ok := bodyFloat(a.state.BodyState, "energy"); ok && e < 30 {
		tags = append(tags, "energy_low")
	}
	if a.state.CurrentGoal != nil {
		tags = append(tags, a.state.CurrentGoal.Tags...)
	}
	return tags
}

func (a *Agent) retrieve(ctx context.Context, tags, kinds []string) []domain.MemoryItem {
	items, err := a.mem.Retrieve(ctx, domain.MemoryQuery{
		Tags: tags, Keywords: tags, Kinds: kinds, Limit: 8,
	})
	if err != nil {
		return nil
	}
	return items
}

// principleTexts returns the texts of retrieved ACTIVE principles for prompting.
func (a *Agent) principleTexts() []any {
	var out []any
	for _, it := range a.lastRetrieved {
		if it.Kind == domain.KindPrinciple && it.Principle != nil && it.Principle.Status == domain.StatusActive {
			out = append(out, it.Principle.Text)
		}
	}
	return out
}

// planFromSkill returns a plan expanded from the highest-scored active skill
// whose tags overlap the goal, if any.
func (a *Agent) planFromSkill(goal domain.Goal) (domain.Plan, bool) {
	for _, it := range a.lastRetrieved {
		if it.Kind != domain.KindSkill || it.Skill == nil {
			continue
		}
		sk := *it.Skill
		if sk.Status != domain.StatusActive {
			continue
		}
		if !tagsOverlap(sk.Tags, a.matchTags()) {
			continue
		}
		if plan, err := a.expander.Expand(goal.ID, sk); err == nil && len(plan.Steps) > 0 {
			return plan, true
		}
	}
	return domain.Plan{}, false
}

func (a *Agent) parsePlanSteps(text string) ([]domain.Action, string) {
	obj, err := llm.ExtractJSONObject(text)
	if err != nil {
		return nil, ""
	}
	var parsed struct {
		Steps []struct {
			Action     string         `json:"action"`
			Parameters map[string]any `json:"parameters"`
		} `json:"steps"`
		Rationale string `json:"rationale"`
	}
	if json.Unmarshal([]byte(obj), &parsed) != nil {
		return nil, ""
	}
	steps := make([]domain.Action, 0, len(parsed.Steps))
	for _, s := range parsed.Steps {
		params := s.Parameters
		if params == nil {
			params = map[string]any{}
		}
		steps = append(steps, domain.Action{Name: s.Action, Parameters: params})
	}
	return steps, parsed.Rationale
}

// validateSteps keeps only steps that pass registry validation (whitelist +
// parameter types). This is the action whitelist enforcement point.
func (a *Agent) validateSteps(steps []domain.Action) []domain.Action {
	out := make([]domain.Action, 0, len(steps))
	for _, s := range steps {
		if s.Parameters == nil {
			s.Parameters = map[string]any{}
		}
		if err := a.registry.Validate(s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// fallbackGoal is the heuristic used when the LLM fails: recover if energy is
// low, otherwise the highest-priority goal.
func (a *Agent) fallbackGoal() domain.Goal {
	if e, ok := bodyFloat(a.state.BodyState, "energy"); ok && e < 30 {
		if g, ok := a.goalByTag("recover"); ok {
			return g
		}
		if g, ok := a.goalByTag("survive"); ok {
			return g
		}
	}
	best := a.goals[0]
	for _, g := range a.goals[1:] {
		if g.Priority > best.Priority {
			best = g
		}
	}
	return best
}

// fallbackSteps returns a single safe action (prefer observe, else any registered).
func (a *Agent) fallbackSteps() []domain.Action {
	names := a.registry.Names()
	for _, n := range names {
		if n == "observe" {
			return []domain.Action{{Name: n, Parameters: map[string]any{}}}
		}
	}
	if len(names) > 0 {
		return []domain.Action{{Name: names[0], Parameters: map[string]any{}}}
	}
	return nil
}

func (a *Agent) goalByID(id string) (domain.Goal, bool) {
	for _, g := range a.goals {
		if g.ID == id {
			return g, true
		}
	}
	return domain.Goal{}, false
}

func (a *Agent) goalByTag(tag string) (domain.Goal, bool) {
	for _, g := range a.goals {
		for _, t := range g.Tags {
			if t == tag {
				return g, true
			}
		}
	}
	return domain.Goal{}, false
}

func tagsOverlap(a, b []string) bool {
	set := map[string]struct{}{}
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
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
