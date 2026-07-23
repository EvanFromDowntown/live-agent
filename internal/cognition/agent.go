// Package cognition implements the Agent: the LLM-driven decision loop. The LLM
// is a frozen cognitive module — cognition only changes what goes into the
// prompt (retrieved memory, state) and strictly validates what comes out
// (JSON schema, action whitelist, parameter types). Every LLM decision has a
// deterministic heuristic fallback so the agent never stalls.
package cognition

import (
	"context"
	"encoding/json"
	"fmt"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/llm"
	"liveagent/internal/safety"
	"liveagent/internal/skills"
)

// Agent is the concrete domain.Agent.
type Agent struct {
	state    *domain.AgentState
	llm      domain.LLM
	mem      domain.Memory
	registry *domain.ActionRegistry
	guard    *safety.Guard
	expander *skills.Expander
	goals    []domain.Goal

	// lastRetrieved caches memory retrieved during SelectGoal for reuse in Plan.
	lastRetrieved []domain.MemoryItem
}

// Deps bundles the collaborators the Agent needs.
type Deps struct {
	State    *domain.AgentState
	LLM      domain.LLM
	Memory   domain.Memory
	Registry *domain.ActionRegistry
	Guard    *safety.Guard
	Expander *skills.Expander
	Goals    []config.GoalConfig
}

// New builds an Agent.
func New(d Deps) *Agent {
	goals := make([]domain.Goal, 0, len(d.Goals))
	for _, g := range d.Goals {
		goals = append(goals, domain.Goal{
			ID: g.ID, Description: g.Description, Priority: g.Priority, Tags: g.Tags, Root: true,
		})
	}
	return &Agent{
		state: d.State, llm: d.LLM, mem: d.Memory, registry: d.Registry,
		guard: d.Guard, expander: d.Expander, goals: goals,
	}
}

// UpdateState folds a new observation into the world model and self model.
func (a *Agent) UpdateState(_ context.Context, obs domain.Observation) error {
	if a.state.WorldState == nil {
		a.state.WorldState = map[string]any{}
	}
	for k, v := range obs.Data {
		a.state.WorldState[k] = v
	}
	if a.state.SelfModel.Beliefs == nil {
		a.state.SelfModel.Beliefs = map[string]any{}
	}
	a.state.SelfModel.Beliefs["last_observation_tick"] = obs.Tick
	return nil
}

// SelectGoal retrieves relevant memory and asks the LLM to choose among the
// human-defined root goals. It never invents new root goals.
func (a *Agent) SelectGoal(ctx context.Context) (domain.Goal, error) {
	a.lastRetrieved = a.retrieve(ctx, a.situationTags(), []string{domain.KindPrinciple, domain.KindSkill})

	goalOpts := make([]map[string]any, 0, len(a.goals))
	for _, g := range a.goals {
		goalOpts = append(goalOpts, map[string]any{"id": g.ID, "tags": toAnySlice(g.Tags), "priority": g.Priority})
	}
	ctxMap := map[string]any{
		"task":       "goal",
		"age":        a.state.Age,
		"body":       a.state.BodyState,
		"goals":      goalOpts,
		"principles": a.principleTexts(),
	}
	req := domain.LLMRequest{
		System:    "You select the agent's next goal from the allowed list. Reply STRICT JSON {\"goal_id\":string,\"reason\":string}.",
		User:      llm.BuildUserMessage("Choose the most appropriate goal id.", ctxMap),
		JSONMode:  true,
		MaxTokens: 200,
	}
	a.state.ResourceUsage.LLMCalls++

	var chosen domain.Goal
	if resp, err := a.llm.Generate(ctx, req); err == nil {
		var parsed struct {
			GoalID string `json:"goal_id"`
			Reason string `json:"reason"`
		}
		if obj, e := llm.ExtractJSONObject(resp.Text); e == nil {
			if json.Unmarshal([]byte(obj), &parsed) == nil {
				if g, ok := a.goalByID(parsed.GoalID); ok {
					chosen = g
				}
			}
		}
	}
	if chosen.ID == "" {
		chosen = a.fallbackGoal() // heuristic degradation
	}
	a.state.CurrentGoal = &chosen
	return chosen, nil
}

// Plan builds an action sequence for the goal. It prefers an active skill whose
// tags match the goal; otherwise it asks the LLM and validates every step.
func (a *Agent) Plan(ctx context.Context, goal domain.Goal) (domain.Plan, error) {
	// 1. Reuse a matching active skill if one exists.
	if plan, ok := a.planFromSkill(goal); ok {
		return plan, nil
	}

	// 2. Ask the LLM for a plan.
	avail := a.registry.Names()
	ctxMap := map[string]any{
		"task":              "plan",
		"goal":              goal.Description,
		"goal_tags":         toAnySlice(goal.Tags),
		"available_actions": toAnySlice(avail),
		"principles":        a.principleTexts(),
		"body":              a.state.BodyState,
	}
	req := domain.LLMRequest{
		System:    "You output a short action plan. Reply STRICT JSON {\"steps\":[{\"action\":string,\"parameters\":object}],\"rationale\":string}. Only use actions from available_actions.",
		User:      llm.BuildUserMessage("Produce a plan.", ctxMap),
		JSONMode:  true,
		MaxTokens: 400,
	}
	a.state.ResourceUsage.LLMCalls++

	var plan domain.Plan
	plan.GoalID = goal.ID
	if resp, err := a.llm.Generate(ctx, req); err == nil {
		plan.Steps, plan.Rationale = a.parsePlanSteps(resp.Text)
	}
	// 3. Validate & drop anything not registered; fall back if empty.
	plan.Steps = a.validateSteps(plan.Steps)
	if len(plan.Steps) == 0 {
		plan.Steps = a.fallbackSteps()
		plan.Rationale = "fallback: no valid LLM plan"
	}
	return plan, nil
}

// SelectAction chooses the next executable action from the plan, skipping any
// action a hard safety constraint forbids under the current body state.
func (a *Agent) SelectAction(_ context.Context, plan domain.Plan) (domain.Action, error) {
	for _, step := range plan.Steps {
		if err := a.registry.Validate(step); err != nil {
			continue
		}
		if d := a.guard.Check(step, a.state.BodyState); !d.Allowed {
			continue // safety overrides — never surface a forbidden action
		}
		return step, nil
	}
	// Degrade to a safe observe/first-available action.
	for _, name := range a.registry.Names() {
		act := domain.Action{Name: name, Parameters: map[string]any{}}
		if a.guard.Check(act, a.state.BodyState).Allowed {
			return act, nil
		}
	}
	return domain.Action{}, fmt.Errorf("cognition: no permissible action available")
}

// Learn appends the experience and updates the self model online (capability
// confidence and risk estimates) — this is Learned state, not weight updates.
func (a *Agent) Learn(ctx context.Context, exp domain.Experience) error {
	if err := a.mem.AppendExperience(ctx, exp); err != nil {
		return err
	}
	if a.state.SelfModel.Capabilities == nil {
		a.state.SelfModel.Capabilities = map[string]float64{}
	}
	if a.state.SelfModel.RiskEstimates == nil {
		a.state.SelfModel.RiskEstimates = map[string]float64{}
	}
	name := exp.Action.Name
	cap := a.state.SelfModel.Capabilities[name]
	target := 0.0
	if exp.Outcome.Success {
		target = 1.0
	}
	a.state.SelfModel.Capabilities[name] = cap + 0.2*(target-cap) // EMA toward outcome
	a.state.SelfModel.RiskEstimates[name] = 0.8*a.state.SelfModel.RiskEstimates[name] + 0.2*exp.Reward.RiskCost
	a.state.ResourceUsage.Actions++
	return nil
}

// State returns the shared state pointer (used by the kernel).
func (a *Agent) State() *domain.AgentState { return a.state }

// LastRetrieved exposes the most recent retrieval for logging.
func (a *Agent) LastRetrieved() []domain.MemoryItem { return a.lastRetrieved }
