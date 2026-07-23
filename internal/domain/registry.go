package domain

import (
	"fmt"
	"sort"
	"sync"
)

// ActionRegistry is the single gate through which every action must pass before
// execution. The runtime NEVER executes an action that is not registered here,
// and it validates parameter names and types against the schema. This is what
// makes "no arbitrary LLM-generated shell/SQL/code" enforceable: the LLM can
// only ever ask for a pre-registered action by name.
type ActionRegistry struct {
	mu      sync.RWMutex
	schemas map[string]ActionSchema
}

// NewActionRegistry creates an empty registry.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{schemas: make(map[string]ActionSchema)}
}

// Register adds (or overwrites) an action schema.
func (r *ActionRegistry) Register(s ActionSchema) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schemas[s.Name] = s
}

// RegisterAll registers a batch of schemas, typically from Environment.AvailableActions.
func (r *ActionRegistry) RegisterAll(schemas []ActionSchema) {
	for _, s := range schemas {
		r.Register(s)
	}
}

// Has reports whether an action name is registered.
func (r *ActionRegistry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.schemas[name]
	return ok
}

// Schema returns the schema for a name.
func (r *ActionRegistry) Schema(name string) (ActionSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.schemas[name]
	return s, ok
}

// Names returns all registered action names, sorted for deterministic output.
func (r *ActionRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.schemas))
	for n := range r.schemas {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Validate checks that an action is registered and that its parameters match
// the schema (required present, no unknown params, correct types). It returns a
// descriptive error on any violation.
func (r *ActionRegistry) Validate(a Action) error {
	r.mu.RLock()
	schema, ok := r.schemas[a.Name]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("action %q is not registered", a.Name)
	}
	for name, spec := range schema.Parameters {
		v, present := a.Parameters[name]
		if !present {
			if spec.Required {
				return fmt.Errorf("action %q missing required parameter %q", a.Name, name)
			}
			continue
		}
		if err := checkType(spec.Type, v); err != nil {
			return fmt.Errorf("action %q parameter %q: %w", a.Name, name, err)
		}
	}
	for name := range a.Parameters {
		if _, known := schema.Parameters[name]; !known {
			return fmt.Errorf("action %q has unknown parameter %q", a.Name, name)
		}
	}
	return nil
}

// checkType performs lenient JSON-friendly type checking. JSON numbers decode to
// float64, so "int" accepts whole-valued float64 as well.
func checkType(t string, v any) error {
	switch t {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("expected string, got %T", v)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", v)
		}
	case "number":
		if !isNumber(v) {
			return fmt.Errorf("expected number, got %T", v)
		}
	case "int":
		switch n := v.(type) {
		case int, int64:
		case float64:
			if n != float64(int64(n)) {
				return fmt.Errorf("expected integer, got fractional %v", n)
			}
		default:
			return fmt.Errorf("expected int, got %T", v)
		}
	case "", "any":
		// no constraint
	default:
		return fmt.Errorf("unknown parameter type %q in schema", t)
	}
	return nil
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, float32, float64:
		return true
	default:
		return false
	}
}
