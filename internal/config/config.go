// Package config loads the human-authored YAML configuration. Everything the
// human controls (root goals, reward weights, safety constraints, body schema,
// loop limits) lives here and is treated as immutable by the Agent at runtime.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration document.
type Config struct {
	Agent       AgentConfig       `yaml:"agent"`
	Runtime     RuntimeConfig     `yaml:"runtime"`
	LLM         LLMConfig         `yaml:"llm"`
	Reward      RewardConfig      `yaml:"reward"`
	Safety      SafetyConfig      `yaml:"safety"`
	Evolution   EvolutionConfig   `yaml:"evolution"`
	Goals       []GoalConfig      `yaml:"goals"`
	Body        BodyConfig        `yaml:"body"`
	Environment EnvironmentConfig `yaml:"environment"`
}

// AgentConfig holds identity/persistence settings.
type AgentConfig struct {
	// Name is a stable human label; the AgentID is derived/persisted so that
	// restarts recover the same identity.
	Name   string `yaml:"name"`
	DBPath string `yaml:"db_path"`
}

// RuntimeConfig controls the main loop.
type RuntimeConfig struct {
	MaxTicks     int64         `yaml:"max_ticks"`     // 0 = unlimited
	TickTimeout  time.Duration `yaml:"tick_timeout"`  // per-tick deadline
	TickInterval time.Duration `yaml:"tick_interval"` // sleep between ticks
	StepMode     bool          `yaml:"step_mode"`     // run a single tick and stop
}

// LLMConfig selects and configures the cognitive module.
type LLMConfig struct {
	// Provider is "fake" or "openai". Secrets come from env vars only.
	Provider    string        `yaml:"provider"`
	Model       string        `yaml:"model"`
	Temperature float64       `yaml:"temperature"`
	MaxTokens   int           `yaml:"max_tokens"`
	Timeout     time.Duration `yaml:"timeout"`
	MaxRetries  int           `yaml:"max_retries"`
}

// RewardConfig holds the weights used to scalarise a RewardVector. Weights are
// human-defined and the Agent may not change them.
type RewardConfig struct {
	Weights RewardWeights `yaml:"weights"`
}

// RewardWeights maps each reward dimension to a scalar weight.
type RewardWeights struct {
	TaskSuccess     float64 `yaml:"task_success"`
	Homeostasis     float64 `yaml:"homeostasis"`
	InformationGain float64 `yaml:"information_gain"`
	HumanFeedback   float64 `yaml:"human_feedback"`
	ResourceCost    float64 `yaml:"resource_cost"`
	RiskCost        float64 `yaml:"risk_cost"`
}

// SafetyConfig defines hard constraints that always override reward.
type SafetyConfig struct {
	Constraints []ConstraintConfig `yaml:"constraints"`
}

// ConstraintConfig is one declarative hard constraint. We only support a small
// set of built-in constraint types so nothing arbitrary is ever evaluated.
type ConstraintConfig struct {
	Name string `yaml:"name"`
	// Type is one of: "forbidden_action", "body_min", "body_max".
	Type string `yaml:"type"`
	// Action is used by "forbidden_action".
	Action string `yaml:"action"`
	// Field/Value are used by body_min / body_max (compares body_state[field]).
	Field string  `yaml:"field"`
	Value float64 `yaml:"value"`
	// AppliesTo optionally restricts a body constraint to a specific action;
	// empty means it applies to every action.
	AppliesTo string `yaml:"applies_to"`
	Reason    string `yaml:"reason"`
}

// EvolutionConfig controls principle/skill lifecycles.
type EvolutionConfig struct {
	// MinEvidence is how many corroborating experiences a principle needs
	// before it may be activated.
	MinEvidence int `yaml:"min_evidence"`
	// MinConfidence is the confidence threshold for activation.
	MinConfidence float64 `yaml:"min_confidence"`
	// MinSuccessRate is the historical success-rate threshold for activation.
	MinSuccessRate float64 `yaml:"min_success_rate"`
}

// GoalConfig is a human-defined (root) goal.
type GoalConfig struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Priority    float64  `yaml:"priority"`
	Tags        []string `yaml:"tags"`
}

// BodyConfig defines the body-state schema. Body fields are NOT hard-coded in
// Go; they are declared here so new embodiments can add fields freely.
type BodyConfig struct {
	Fields []BodyField `yaml:"fields"`
}

// BodyField declares one proprioceptive/body variable.
type BodyField struct {
	Name    string  `yaml:"name"`
	Initial float64 `yaml:"initial"`
	Min     float64 `yaml:"min"`
	Max     float64 `yaml:"max"`
}

// EnvironmentConfig is a free-form map interpreted by the selected environment.
type EnvironmentConfig struct {
	Type   string         `yaml:"type"`
	Params map[string]any `yaml:"params"`
}

// Load reads and validates a YAML config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Agent.Name == "" {
		c.Agent.Name = "agent"
	}
	if c.Agent.DBPath == "" {
		c.Agent.DBPath = "agent.db"
	}
	if c.Runtime.TickTimeout == 0 {
		c.Runtime.TickTimeout = 30 * time.Second
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = "fake"
	}
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = 30 * time.Second
	}
	if c.LLM.MaxRetries == 0 {
		c.LLM.MaxRetries = 1
	}
	if c.LLM.MaxTokens == 0 {
		c.LLM.MaxTokens = 1024
	}
	if c.Evolution.MinEvidence == 0 {
		c.Evolution.MinEvidence = 2
	}
	if c.Evolution.MinConfidence == 0 {
		c.Evolution.MinConfidence = 0.6
	}
}

func (c *Config) validate() error {
	if len(c.Goals) == 0 {
		return fmt.Errorf("config: at least one goal must be defined")
	}
	switch c.LLM.Provider {
	case "fake", "openai":
	default:
		return fmt.Errorf("config: unknown llm.provider %q", c.LLM.Provider)
	}
	for _, con := range c.Safety.Constraints {
		switch con.Type {
		case "forbidden_action", "body_min", "body_max":
		default:
			return fmt.Errorf("config: unknown constraint type %q", con.Type)
		}
	}
	return nil
}
