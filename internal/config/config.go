// Package config loads the agent's runtime configuration from YAML. The config
// is deliberately small: everything task-specific (the actual task text) comes
// in at runtime via stdin, not from a file. Credentials are NEVER read from
// config — the LLM provider reads them from environment variables only.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole configuration tree.
type Config struct {
	Agent   AgentConfig   `yaml:"agent"`
	LLM     LLMConfig     `yaml:"llm"`
	Limits  LimitsConfig  `yaml:"limits"`
	Context ContextConfig `yaml:"context"`
	Safety  SafetyConfig  `yaml:"safety"`
	Verify  VerifyConfig  `yaml:"verify"`
	Learn   LearnConfig   `yaml:"learn"`
}

// AgentConfig identifies the agent and where it persists state / runs work.
type AgentConfig struct {
	// Name is a stable human label used to recover the same identity on restart.
	Name string `yaml:"name"`
	// DBPath is the SQLite file for events/episodes/notes.
	DBPath string `yaml:"db_path"`
	// Workspace is the directory shell/python actions run in. Created if missing.
	Workspace string `yaml:"workspace"`
	// SystemPrompt OPTIONALLY extends (appends to) the built-in, task-agnostic
	// operating prompt. Empty uses the built-in default alone.
	SystemPrompt string `yaml:"system_prompt"`
}

// LLMConfig configures the cognitive module. Provider "openai" reads
// LLM_BASE_URL / LLM_API_KEY / LLM_MODEL from the environment.
type LLMConfig struct {
	Provider    string        `yaml:"provider"`
	Model       string        `yaml:"model"`
	APIPath     string        `yaml:"api_path"` // request path override (default "/chat/completions")
	Timeout     time.Duration `yaml:"timeout"`
	MaxRetries  int           `yaml:"max_retries"`
	Temperature float64       `yaml:"temperature"`
	// OmitTemperature drops the temperature field from requests entirely. Set it
	// for models that only accept their default temperature (e.g. some reasoning
	// models) and reject any explicit value with a 400.
	OmitTemperature bool `yaml:"omit_temperature"`
	MaxTokens       int  `yaml:"max_tokens"`
	// DisableResponseFormat avoids response_format:json_object on gateways that
	// corrupt the request; irrelevant while tool-calling is active.
	DisableResponseFormat bool `yaml:"disable_response_format"`
	// DisableThinking / ReasoningEffort bound reasoning-model token spend.
	DisableThinking bool   `yaml:"disable_thinking"`
	ReasoningEffort string `yaml:"reasoning_effort"` // "", "low", "medium", "high"
}

// LimitsConfig bounds a run so it always terminates.
type LimitsConfig struct {
	// MaxSteps caps the number of tool calls in one run.
	MaxSteps int `yaml:"max_steps"`
	// MaxRepeats stops the run when the SAME tool call (name+args) is emitted
	// this many times in a row (spinning in place).
	MaxRepeats int `yaml:"max_repeats"`
	// MaxConsecutiveErrors stops the run after this many failing steps in a row.
	MaxConsecutiveErrors int `yaml:"max_consecutive_errors"`
	// StallNudge injects a convergence warning into the ask after this many
	// consecutive UNPRODUCTIVE steps (errors or byte-identical repeated output).
	// It does not stop the run; it prods the model to bank results / change tack.
	StallNudge int `yaml:"stall_nudge"`
	// StepTimeout bounds a single shell/python action.
	StepTimeout time.Duration `yaml:"step_timeout"`
}

// VerifyConfig controls objective, independent verification of a finish claim
// (LLM-as-judge). It makes the reported success signal meaningful instead of a
// pure self-report.
type VerifyConfig struct {
	// Enabled turns on verification of finish(success=true) claims. Omitted
	// defaults to true; set explicitly to false to disable.
	Enabled *bool `yaml:"enabled"`
	// MaxRejections caps how many times a finish may be rejected and the run
	// forced to continue before the finish is accepted (as unverified).
	MaxRejections int `yaml:"max_rejections"`
}

// On reports whether verification is enabled (nil => default on).
func (v VerifyConfig) On() bool { return v.Enabled == nil || *v.Enabled }

// LearnConfig controls the verified-learning loop: distilling reusable lessons
// from VERIFIED-successful runs and recalling them into later similar tasks.
type LearnConfig struct {
	// Enabled turns learning on. Omitted defaults to true.
	Enabled *bool `yaml:"enabled"`
	// MaxInject caps how many recalled lessons are injected into a run's context.
	MaxInject int `yaml:"max_inject"`
	// EvictMinUses prunes lessons surfaced at least this many times that never
	// contributed to a verified win (wins=0). Zero disables eviction.
	EvictMinUses int `yaml:"evict_min_uses"`
	// EmbedModel enables semantic recall via an OpenAI-compatible /embeddings
	// endpoint (credentials from env). Empty (and no LLM_EMBED_MODEL env) falls
	// back to lexical word-overlap recall.
	EmbedModel string `yaml:"embed_model"`
}

// On reports whether learning is enabled (nil => default on).
func (l LearnConfig) On() bool { return l.Enabled == nil || *l.Enabled }

// ContextConfig controls the rolling transcript / compaction.
type ContextConfig struct {
	// MaxChars is the transcript budget (~tokens*4). Compaction fires at
	// CompactAtPct of this. Zero uses a sensible default.
	MaxChars int `yaml:"max_chars"`
	// KeepRecent is how many recent turns are kept verbatim during compaction.
	KeepRecent int `yaml:"keep_recent"`
	// CompactAtPct (0..1) is the fraction of MaxChars that triggers compaction.
	CompactAtPct float64 `yaml:"compact_at_pct"`
}

// SafetyConfig holds hard constraints. A hard constraint ALWAYS overrides the
// model's wish: a matching action is rejected before execution.
type SafetyConfig struct {
	// ForbiddenActions are tool names the agent may never call.
	ForbiddenActions []string `yaml:"forbidden_actions"`
	// ForbiddenShellPatterns are substrings that, if present in a run_shell /
	// run_python payload, cause rejection (e.g. "rm -rf /", ":(){").
	ForbiddenShellPatterns []string `yaml:"forbidden_shell_patterns"`
}

// Load reads and validates a YAML config, filling in defaults.
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
	return &c, nil
}

// Default returns a config with only defaults applied (no file).
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.Agent.Name == "" {
		c.Agent.Name = "agent"
	}
	if c.Agent.DBPath == "" {
		c.Agent.DBPath = "agent.db"
	}
	if c.Agent.Workspace == "" {
		c.Agent.Workspace = "workspace"
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = "openai"
	}
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = 180 * time.Second
	}
	if c.LLM.MaxTokens == 0 {
		c.LLM.MaxTokens = 4096
	}
	if c.LLM.MaxRetries == 0 {
		c.LLM.MaxRetries = 2
	}
	if c.Limits.MaxSteps == 0 {
		c.Limits.MaxSteps = 30
	}
	if c.Limits.MaxRepeats == 0 {
		c.Limits.MaxRepeats = 3
	}
	if c.Limits.MaxConsecutiveErrors == 0 {
		c.Limits.MaxConsecutiveErrors = 6
	}
	if c.Limits.StallNudge == 0 {
		c.Limits.StallNudge = 5
	}
	if c.Limits.StepTimeout == 0 {
		c.Limits.StepTimeout = 120 * time.Second
	}
	if c.Verify.MaxRejections == 0 {
		c.Verify.MaxRejections = 2
	}
	if c.Context.MaxChars == 0 {
		c.Context.MaxChars = 48000
	}
	if c.Context.KeepRecent == 0 {
		c.Context.KeepRecent = 6
	}
	if c.Context.CompactAtPct == 0 {
		c.Context.CompactAtPct = 0.75
	}
	if c.Learn.MaxInject == 0 {
		c.Learn.MaxInject = 3
	}
	// EvictMinUses intentionally has no default: 0 disables eviction. Set it in
	// config to enable pruning of chronically-unhelpful lessons.
}
