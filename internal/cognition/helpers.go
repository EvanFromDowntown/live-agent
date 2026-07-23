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

// parsePlanSteps leniently extracts an action plan from LLM output. Real models
// vary a lot in shape, so we tolerate: steps under "steps"/"plan"/"actions", the
// action name under "action"/"name"/"tool", and parameters under
// "parameters"/"params"/"arguments"/"input" — or, failing that, any leftover
// keys on the step object are treated as the parameters.
func (a *Agent) parsePlanSteps(text string) ([]domain.Action, string) {
	obj, err := llm.ExtractJSONObject(text)
	if err != nil {
		return nil, ""
	}
	var root map[string]any
	if json.Unmarshal([]byte(obj), &root) != nil {
		return nil, ""
	}
	rationale, _ := root["rationale"].(string)

	var rawSteps []any
	for _, key := range []string{"steps", "plan", "actions"} {
		if v, ok := root[key].([]any); ok {
			rawSteps = v
			break
		}
	}
	// Some models return a single action object instead of a list.
	if rawSteps == nil {
		if _, ok := firstKey(root, "action", "name", "tool"); ok {
			rawSteps = []any{root}
		}
	}

	steps := make([]domain.Action, 0, len(rawSteps))
	for _, rs := range rawSteps {
		m, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		name, ok := firstKey(m, "action", "name", "tool")
		if !ok || name == "" {
			continue
		}
		params := extractParams(m)
		steps = append(steps, domain.Action{Name: name, Parameters: params})
	}
	return steps, rationale
}

func firstKey(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	return "", false
}

func extractParams(step map[string]any) map[string]any {
	for _, k := range []string{"parameters", "params", "arguments", "args", "input"} {
		if p, ok := step[k].(map[string]any); ok {
			return p
		}
	}
	// Fall back to leftover keys (everything except recognised control fields).
	control := map[string]bool{"action": true, "name": true, "tool": true, "rationale": true, "reason": true, "thought": true}
	params := map[string]any{}
	for k, v := range step {
		if !control[k] {
			params[k] = v
		}
	}
	return params
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

// fallbackSteps returns a single safe action. It prefers a passive, non-terminal
// action (observe/read_file) and NEVER falls back to a terminal action like
// "finish", which would end an episode without doing any work.
func (a *Agent) fallbackSteps() []domain.Action {
	names := a.registry.Names()
	prefer := []string{"observe", "read_file", "rest"}
	for _, p := range prefer {
		for _, n := range names {
			if n == p {
				return []domain.Action{{Name: n, Parameters: map[string]any{}}}
			}
		}
	}
	// Otherwise the first non-terminal registered action with no required params.
	for _, n := range names {
		if n == "finish" || n == "done" || n == "stop" {
			continue
		}
		if sc, ok := a.registry.Schema(n); ok && !hasRequired(sc) {
			return []domain.Action{{Name: n, Parameters: map[string]any{}}}
		}
	}
	return nil
}

func hasRequired(s domain.ActionSchema) bool {
	for _, p := range s.Parameters {
		if p.Required {
			return true
		}
	}
	return false
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

// compactAny shrinks a value for prompting: long strings are truncated and long
// slices are capped, so a huge WorldState (e.g. large last_stdout) cannot blow
// up the prompt and starve a reasoning model's token budget.
func compactAny(v any, maxStr, maxLen int) any {
	switch x := v.(type) {
	case string:
		if len(x) > maxStr {
			return x[:maxStr] + "…[truncated]"
		}
		return x
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			m[k] = compactAny(vv, maxStr, maxLen)
		}
		return m
	case []any:
		if len(x) > maxLen {
			x = x[:maxLen]
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = compactAny(e, maxStr, maxLen)
		}
		return out
	default:
		return v
	}
}

func (a *Agent) compactWorld() any {
	return compactAny(a.state.WorldState, 600, 40)
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
