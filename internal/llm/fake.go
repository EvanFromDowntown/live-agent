package llm

import (
	"context"
	"encoding/json"

	"liveagent/internal/domain"
)

// FakeLLM is a deterministic, offline cognitive module. It reads the structured
// context block that cognition embeds and returns valid, schema-conformant JSON
// for each task (goal / plan / reflect). This lets the whole system — unit tests
// and the GridWorld demo — run with NO API key and NO network.
//
// It is intentionally rule-based rather than random so behaviour is reproducible.
type FakeLLM struct{}

// NewFakeLLM constructs a FakeLLM.
func NewFakeLLM() *FakeLLM { return &FakeLLM{} }

// energyLowThreshold: below this the fake "brain" prioritises recovery.
const energyLowThreshold = 30.0

// Generate implements domain.LLM.
func (f *FakeLLM) Generate(_ context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	ctxMap := ExtractContext(req.User)
	task, _ := ctxMap["task"].(string)

	var out any
	switch task {
	case "goal":
		out = f.decideGoal(ctxMap)
	case "plan":
		out = f.decidePlan(ctxMap)
	case "reflect":
		out = f.decideReflection(ctxMap)
	default:
		out = map[string]any{"note": "no-op"}
	}
	b, _ := json.Marshal(out)
	return domain.LLMResponse{Text: string(b), TokensUsed: len(b) / 4}, nil
}

// decideGoal picks a root goal. When body energy is low it prefers a goal tagged
// "recover" or "survive"; otherwise it prefers one tagged "explore".
func (f *FakeLLM) decideGoal(ctx map[string]any) map[string]any {
	goals := asMaps(ctx["goals"])
	low := energyIsLow(ctx)

	pick := func(tag string) (string, bool) {
		for _, g := range goals {
			for _, t := range asStrings(g["tags"]) {
				if t == tag {
					return asString(g["id"]), true
				}
			}
		}
		return "", false
	}

	if low {
		if id, ok := pick("recover"); ok {
			return map[string]any{"goal_id": id, "reason": "energy is low, prioritise recovery"}
		}
		if id, ok := pick("survive"); ok {
			return map[string]any{"goal_id": id, "reason": "energy is low, prioritise survival"}
		}
	}
	if id, ok := pick("explore"); ok {
		return map[string]any{"goal_id": id, "reason": "energy sufficient, gather information"}
	}
	if len(goals) > 0 {
		return map[string]any{"goal_id": asString(goals[0]["id"]), "reason": "default goal"}
	}
	return map[string]any{"goal_id": "", "reason": "no goals configured"}
}

// decidePlan produces an ordered action sequence, constrained to the available
// actions. It also consults retrieved principles: if a principle about resting
// when energy is low is present and energy is low, it plans a recovery sequence.
func (f *FakeLLM) decidePlan(ctx map[string]any) map[string]any {
	avail := map[string]bool{}
	for _, a := range asStrings(ctx["available_actions"]) {
		avail[a] = true
	}
	low := energyIsLow(ctx)
	goalTags := goalTagsFromCtx(ctx)

	var order []string
	rationale := ""
	if low || hasTag(goalTags, "recover") || hasTag(goalTags, "survive") {
		// Recovery: rest reliably restores energy; collect if a resource is here.
		order = []string{"rest", "collect", "explore"}
		rationale = "energy low: rest to recover (collect if a resource is present)"
	} else {
		order = []string{"explore", "move", "observe"}
		rationale = "explore unknown territory to gain information"
	}

	steps := []map[string]any{}
	for _, name := range order {
		if avail[name] {
			steps = append(steps, map[string]any{"action": name, "parameters": map[string]any{}})
		}
	}
	if len(steps) == 0 {
		// Fallback: pick any available action so the loop always progresses.
		for _, a := range asStrings(ctx["available_actions"]) {
			steps = append(steps, map[string]any{"action": a, "parameters": map[string]any{}})
			break
		}
	}
	return map[string]any{"steps": steps, "rationale": rationale}
}

// decideReflection returns the strict reflection JSON contract.
func (f *FakeLLM) decideReflection(ctx map[string]any) map[string]any {
	ep, _ := ctx["episode"].(map[string]any)
	success := false
	energyLow := false
	if ep != nil {
		success, _ = ep["success"].(bool)
		energyLow, _ = ep["energy_low"].(bool)
	}

	if !success && energyLow {
		return map[string]any{
			"success":               false,
			"cause":                 "energy was depleted before the goal was achieved",
			"principle":             "When energy is low, recover it (collect resources or rest) before continuing to explore.",
			"applicable_conditions": []string{"energy_low", "long_horizon_task"},
			"confidence":            0.8,
			"suggested_skill": map[string]any{
				"name": "recover_energy",
				"steps": []map[string]any{
					{"action": "rest", "parameters": map[string]any{}},
					{"action": "collect", "parameters": map[string]any{}},
				},
			},
		}
	}
	if success {
		return map[string]any{
			"success":               true,
			"cause":                 "goal achieved by exploring while keeping energy above the safety margin",
			"principle":             "Interleave exploration with resource collection to sustain energy over long tasks.",
			"applicable_conditions": []string{"exploration_task"},
			"confidence":            0.7,
			"suggested_skill":       nil,
		}
	}
	return map[string]any{
		"success":               false,
		"cause":                 "goal not achieved within the episode",
		"principle":             "Break long tasks into shorter sub-goals and reassess frequently.",
		"applicable_conditions": []string{"long_horizon_task"},
		"confidence":            0.6,
		"suggested_skill":       nil,
	}
}

// ---- small typed accessors over the generic context map -------------------

func energyIsLow(ctx map[string]any) bool {
	body, _ := ctx["body"].(map[string]any)
	if body == nil {
		return false
	}
	if e, ok := toFloat(body["energy"]); ok {
		return e < energyLowThreshold
	}
	return false
}

func goalTagsFromCtx(ctx map[string]any) []string {
	return asStrings(ctx["goal_tags"])
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func asMaps(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func asStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
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
