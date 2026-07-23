// Package domain defines the core, dependency-free types and interfaces that
// every other package builds on. Keeping these here avoids import cycles: all
// concrete packages depend on domain, never the other way around.
package domain

import "time"

// -----------------------------------------------------------------------------
// Perception & action
// -----------------------------------------------------------------------------

// Observation is a single snapshot the Agent receives from the Environment.
type Observation struct {
	Tick int64          `json:"tick"`
	Data map[string]any `json:"data"`
	Text string         `json:"text"`
}

// ParamSpec describes one parameter of an action for validation purposes.
type ParamSpec struct {
	Type        string `json:"type"` // "string" | "number" | "int" | "bool"
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// ActionSchema is the machine-readable contract of an executable action. Only
// actions with a registered schema may ever be executed (see ActionRegistry).
type ActionSchema struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]ParamSpec `json:"parameters"`
}

// Action is a concrete, parameterised action the Agent wants to perform.
type Action struct {
	Name       string         `json:"action"`
	Parameters map[string]any `json:"parameters"`
}

// Outcome is the result of executing an Action against the Environment.
type Outcome struct {
	Success     bool           `json:"success"`
	Observation Observation    `json:"observation"`
	Reward      RewardVector   `json:"reward"`
	Info        map[string]any `json:"info"`
	Text        string         `json:"text"`
}

// -----------------------------------------------------------------------------
// Reward
// -----------------------------------------------------------------------------

// RewardVector is a multi-dimensional reward. We never collapse this into a
// single number at the source; scalarisation happens via configured weights.
type RewardVector struct {
	TaskSuccess     float64 `json:"task_success"`
	Homeostasis     float64 `json:"homeostasis"`
	InformationGain float64 `json:"information_gain"`
	HumanFeedback   float64 `json:"human_feedback"`
	ResourceCost    float64 `json:"resource_cost"`
	RiskCost        float64 `json:"risk_cost"`
}

// -----------------------------------------------------------------------------
// Goals & plans
// -----------------------------------------------------------------------------

// Goal is something the Agent is trying to achieve. Root goals are defined by
// humans in config and are immutable; the Agent only selects among them.
type Goal struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Priority    float64  `json:"priority"`
	Tags        []string `json:"tags"`
	Root        bool     `json:"root"`
}

// Plan is an ordered sequence of actions intended to satisfy a Goal.
type Plan struct {
	GoalID    string   `json:"goal_id"`
	Steps     []Action `json:"steps"`
	Rationale string   `json:"rationale"`
	SkillName string   `json:"skill_name,omitempty"`
}

// -----------------------------------------------------------------------------
// Memory records
// -----------------------------------------------------------------------------

// Experience is a single (goal, observation, action, outcome) tuple. Raw
// experiences are append-only and never mutated.
type Experience struct {
	ID          string       `json:"id"`
	AgentID     string       `json:"agent_id"`
	EpisodeID   string       `json:"episode_id"`
	Tick        int64        `json:"tick"`
	Goal        string       `json:"goal"`
	Observation Observation  `json:"observation"`
	Action      Action       `json:"action"`
	Outcome     Outcome      `json:"outcome"`
	Reward      RewardVector `json:"reward"`
	Tags        []string     `json:"tags"`
	CreatedAt   time.Time    `json:"created_at"`
}

// Episode is the full trajectory of one task attempt.
type Episode struct {
	ID          string       `json:"id"`
	AgentID     string       `json:"agent_id"`
	Goal        string       `json:"goal"`
	StartTick   int64        `json:"start_tick"`
	EndTick     int64        `json:"end_tick"`
	Success     bool         `json:"success"`
	TotalReward float64      `json:"total_reward"`
	Trace       []Experience `json:"trace"`
	Summary     string       `json:"summary"`
	Tags        []string     `json:"tags"`
	CreatedAt   time.Time    `json:"created_at"`
}

// Principle lifecycle statuses.
const (
	StatusCandidate  = "candidate"
	StatusEvaluated  = "evaluated"
	StatusActive     = "active"
	StatusDeprecated = "deprecated"
)

// Principle is a reusable, language-level rule distilled from experience.
type Principle struct {
	ID                   string    `json:"id"`
	AgentID              string    `json:"agent_id"`
	Text                 string    `json:"text"`
	ApplicableConditions []string  `json:"applicable_conditions"`
	Confidence           float64   `json:"confidence"`
	Status               string    `json:"status"`
	Evidence             int       `json:"evidence"`
	SuccessRate          float64   `json:"success_rate"`
	Tags                 []string  `json:"tags"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// SkillStep references a single registered action. It may NOT contain arbitrary
// code — only an action name plus parameters.
type SkillStep struct {
	Action     string         `json:"action"`
	Parameters map[string]any `json:"parameters"`
}

// Skill is a validated, reusable procedure expressed purely as a sequence of
// registered actions (a restricted JSON DSL).
type Skill struct {
	ID                string      `json:"id"`
	AgentID           string      `json:"agent_id"`
	Name              string      `json:"name"`
	Preconditions     []string    `json:"preconditions"`
	Steps             []SkillStep `json:"steps"`
	SuccessConditions []string    `json:"success_conditions"`
	Status            string      `json:"status"`
	Confidence        float64     `json:"confidence"`
	Tags              []string    `json:"tags"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

// -----------------------------------------------------------------------------
// Retrieval
// -----------------------------------------------------------------------------

// Memory item kinds.
const (
	KindExperience = "experience"
	KindEpisode    = "episode"
	KindPrinciple  = "principle"
	KindSkill      = "skill"
)

// MemoryQuery is the request passed to Memory.Retrieve.
type MemoryQuery struct {
	Tags     []string  `json:"tags"`
	Keywords []string  `json:"keywords"`
	Kinds    []string  `json:"kinds"`
	Limit    int       `json:"limit"`
	Now      time.Time `json:"now"`
}

// MemoryItem is a scored retrieval result. Exactly one of the pointer fields is
// populated, according to Kind.
type MemoryItem struct {
	Kind       string      `json:"kind"`
	ID         string      `json:"id"`
	Score      float64     `json:"score"`
	Text       string      `json:"text"`
	Principle  *Principle  `json:"principle,omitempty"`
	Skill      *Skill      `json:"skill,omitempty"`
	Experience *Experience `json:"experience,omitempty"`
	Episode    *Episode    `json:"episode,omitempty"`
}

// -----------------------------------------------------------------------------
// Agent state
// -----------------------------------------------------------------------------

// SelfModel holds the Agent's learned beliefs about itself. All fields are
// Learned state and may be updated from experience.
type SelfModel struct {
	Capabilities  map[string]float64 `json:"capabilities"`   // capability -> confidence
	Beliefs       map[string]any     `json:"beliefs"`        // free-form learned beliefs
	RiskEstimates map[string]float64 `json:"risk_estimates"` // action -> predicted risk
}

// ResourceUsage tracks cumulative resource consumption for auditing/limits.
type ResourceUsage struct {
	LLMCalls   int64 `json:"llm_calls"`
	Ticks      int64 `json:"ticks"`
	TokensUsed int64 `json:"tokens_used"`
	Actions    int64 `json:"actions"`
}

// AgentState is the complete, persistable state of a running agent.
type AgentState struct {
	AgentID       string         `json:"agent_id"`
	CreatedAt     time.Time      `json:"created_at"`
	Age           int64          `json:"age"`
	CurrentGoal   *Goal          `json:"current_goal"`
	WorldState    map[string]any `json:"world_state"`
	BodyState     map[string]any `json:"body_state"`
	SelfModel     SelfModel      `json:"self_model"`
	ActivePolicy  string         `json:"active_policy"`
	ActiveSkills  []string       `json:"active_skills"`
	ResourceUsage ResourceUsage  `json:"resource_usage"`
}

// -----------------------------------------------------------------------------
// Evaluation
// -----------------------------------------------------------------------------

// EvalResult is the outcome of evaluating a principle or skill.
type EvalResult struct {
	Passed  bool           `json:"passed"`
	Score   float64        `json:"score"`
	Reason  string         `json:"reason"`
	Details map[string]any `json:"details"`
}

// -----------------------------------------------------------------------------
// Embodiment / capabilities
// -----------------------------------------------------------------------------

// Capability describes something the body can do.
type Capability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// -----------------------------------------------------------------------------
// LLM
// -----------------------------------------------------------------------------

// LLMRequest is a single chat-completion style request.
type LLMRequest struct {
	System      string  `json:"system"`
	User        string  `json:"user"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	JSONMode    bool    `json:"json_mode"`
}

// LLMResponse is the model's reply.
type LLMResponse struct {
	Text       string `json:"text"`
	TokensUsed int    `json:"tokens_used"`
}
