// Package tool defines the executable capability surface of the agent: a Tool
// interface, a registry that validates calls against schemas, and the built-in
// tools. Native function-calling definitions are derived from the same schemas,
// so the model can only ever call a pre-registered, validated tool.
package tool

import (
	"context"
	"fmt"

	"liveagent/internal/domain"
)

// PlanItem is one entry in the agent's TODO/plan list.
type PlanItem struct {
	Step   string `json:"step"`
	Status string `json:"status"` // "pending" | "in_progress" | "done"
}

// Result is the outcome of executing a tool.
type Result struct {
	// Output is the text shown back to the model (stdout/stderr/return value).
	Output string
	// IsError marks an execution failure (non-zero exit, exception, bad args).
	IsError bool
	// Finished is set by the finishing tool to end the run.
	Finished bool
	// Success is the model's claim of goal achievement (finish tool only).
	Success bool
	// Reply marks a direct conversational answer (the reply tool): the turn ends
	// immediately with no task verification. Output carries the answer text.
	Reply bool
	// Plan / HasPlan are set by update_plan to replace the pinned plan.
	Plan    []PlanItem
	HasPlan bool
	// Title is set by set_title to (re)name the session/conversation.
	Title string
	// SendFile / Caption are set by send_file to surface a workspace file inline
	// in the chat (images shown inline, other files as a download link).
	SendFile string
	Caption  string
}

// Tool is one executable capability.
type Tool interface {
	Spec() domain.ActionSchema
	Execute(ctx context.Context, args map[string]any) Result
}

// Toolset holds the registered tools and gates every call through schema
// validation before execution.
type Toolset struct {
	tools    map[string]Tool
	order    []string
	registry *domain.ActionRegistry
}

// NewToolset creates an empty toolset.
func NewToolset() *Toolset {
	return &Toolset{tools: map[string]Tool{}, registry: domain.NewActionRegistry()}
}

// Register adds a tool (idempotent by name).
func (t *Toolset) Register(tool Tool) {
	spec := tool.Spec()
	if _, exists := t.tools[spec.Name]; !exists {
		t.order = append(t.order, spec.Name)
	}
	t.tools[spec.Name] = tool
	t.registry.Register(spec)
}

// Has reports whether a tool name is registered.
func (t *Toolset) Has(name string) bool {
	_, ok := t.tools[name]
	return ok
}

// Names returns registered tool names in registration order.
func (t *Toolset) Names() []string {
	out := make([]string, len(t.order))
	copy(out, t.order)
	return out
}

// Defs converts the registered schemas into native function-calling tool
// definitions (JSON-Schema parameter objects).
func (t *Toolset) Defs() []domain.ToolDef {
	defs := make([]domain.ToolDef, 0, len(t.order))
	for _, name := range t.order {
		sc, ok := t.registry.Schema(name)
		if !ok {
			continue
		}
		props := map[string]any{}
		var required []string
		for pname, spec := range sc.Parameters {
			p := map[string]any{}
			if jt := jsonSchemaType(spec.Type); jt != "" {
				p["type"] = jt
			}
			if spec.Description != "" {
				p["description"] = spec.Description
			}
			props[pname] = p
			if spec.Required {
				required = append(required, pname)
			}
		}
		params := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			params["required"] = required
		}
		defs = append(defs, domain.ToolDef{Name: sc.Name, Description: sc.Description, Parameters: params})
	}
	return defs
}

// Validate checks an action against its schema.
func (t *Toolset) Validate(a domain.Action) error {
	return t.registry.Validate(a)
}

// Execute validates then runs the action. On a validation failure it returns an
// error Result rather than panicking, so the model sees the mistake and can fix
// it next turn.
func (t *Toolset) Execute(ctx context.Context, a domain.Action) Result {
	tool, ok := t.tools[a.Name]
	if !ok {
		return Result{IsError: true, Output: fmt.Sprintf("unknown tool %q", a.Name)}
	}
	if err := t.registry.Validate(a); err != nil {
		return Result{IsError: true, Output: "invalid arguments: " + err.Error()}
	}
	return tool.Execute(ctx, a.Parameters)
}

func jsonSchemaType(t string) string {
	switch t {
	case "string":
		return "string"
	case "bool":
		return "boolean"
	case "number":
		return "number"
	case "int":
		return "integer"
	default:
		return ""
	}
}
