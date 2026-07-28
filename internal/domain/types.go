// Package domain defines the core, dependency-free types and interfaces that
// every other package builds on. Keeping these here avoids import cycles: all
// concrete packages depend on domain, never the other way around.
package domain

import "context"

// -----------------------------------------------------------------------------
// Actions / tools
// -----------------------------------------------------------------------------

// ParamSpec describes one parameter of an action for validation purposes.
type ParamSpec struct {
	Type        string `json:"type"` // "string" | "number" | "int" | "bool"
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// ActionSchema is the machine-readable contract of an executable action/tool.
// Only actions with a registered schema may ever be executed (see
// ActionRegistry). It doubles as the source for native function-calling tool
// definitions.
type ActionSchema struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]ParamSpec `json:"parameters"`
}

// Action is a concrete, parameterised action the agent wants to perform.
type Action struct {
	Name       string         `json:"action"`
	Parameters map[string]any `json:"parameters"`
}

// -----------------------------------------------------------------------------
// LLM
// -----------------------------------------------------------------------------

// Message is one chat turn. Role is "system", "user", or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolDef describes a callable tool (native function-calling). Parameters is a
// JSON-Schema object. Built from the ActionRegistry so the model can only ever
// call a pre-registered, schema-validated action.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a single native tool call emitted by the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON object string
}

// LLMRequest is a chat-completion style request. Messages carries the full
// (multi-turn) conversation: a system message, prior assistant/user turns (the
// rolling episode transcript), and the current user ask last. When Tools is set,
// the provider advertises native function-calling and the model may answer with
// ToolCalls instead of (or in addition to) text.
type LLMRequest struct {
	Messages    []Message `json:"messages"`
	Tools       []ToolDef `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"` // "", "auto", "required", "none"
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	JSONMode    bool      `json:"json_mode"`
}

// LLMResponse is the model's reply. ToolCalls is populated when the model used
// native function-calling.
type LLMResponse struct {
	Text       string     `json:"text"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	TokensUsed int        `json:"tokens_used"`
}

// LLM is the frozen cognitive module. It is never fine-tuned; we only change
// what we put into the prompt.
type LLM interface {
	Generate(ctx context.Context, request LLMRequest) (LLMResponse, error)
}
