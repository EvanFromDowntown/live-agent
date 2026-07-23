package domain

import "context"

// Environment is the world the Agent acts in. It is fully replaceable; the
// runtime only ever talks to it through this interface.
type Environment interface {
	Observe(ctx context.Context) (Observation, error)
	AvailableActions(ctx context.Context) []ActionSchema
	Execute(ctx context.Context, action Action) (Outcome, error)
	Tick() int64
}

// Embodiment is the Agent's body adapter: capabilities, proprioception and
// health. Kept separate from Environment so the same body can move between
// environments.
type Embodiment interface {
	Capabilities(ctx context.Context) []Capability
	Proprioception(ctx context.Context) (map[string]any, error)
	Health(ctx context.Context) (map[string]any, error)
}

// LLM is the frozen cognitive module. It is never fine-tuned; we only change
// what we put into the prompt.
type LLM interface {
	Generate(ctx context.Context, request LLMRequest) (LLMResponse, error)
}

// Memory persists and retrieves the Agent's experience and knowledge.
type Memory interface {
	AppendExperience(ctx context.Context, exp Experience) error
	Retrieve(ctx context.Context, query MemoryQuery) ([]MemoryItem, error)
	SavePrinciple(ctx context.Context, principle Principle) error
	SaveSkill(ctx context.Context, skill Skill) error
	Consolidate(ctx context.Context) error
}

// Evaluator gates candidate principles and skills before they can activate.
type Evaluator interface {
	EvaluatePrinciple(ctx context.Context, principle Principle) EvalResult
	EvaluateSkill(ctx context.Context, skill Skill) EvalResult
}

// Agent is the cognitive decision loop. The kernel drives it; it does not run
// itself.
type Agent interface {
	UpdateState(ctx context.Context, obs Observation) error
	SelectGoal(ctx context.Context) (Goal, error)
	Plan(ctx context.Context, goal Goal) (Plan, error)
	SelectAction(ctx context.Context, plan Plan) (Action, error)
	Learn(ctx context.Context, exp Experience) error
}
