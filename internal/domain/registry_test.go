package domain

import "testing"

func testRegistry() *ActionRegistry {
	r := NewActionRegistry()
	r.Register(ActionSchema{
		Name: "move",
		Parameters: map[string]ParamSpec{
			"direction": {Type: "string", Required: true},
			"steps":     {Type: "int", Required: false},
		},
	})
	return r
}

// Criterion 3: illegal actions are rejected by the registry.
func TestRegistryRejectsUnregisteredAction(t *testing.T) {
	r := testRegistry()
	if err := r.Validate(Action{Name: "rm_rf", Parameters: map[string]any{}}); err == nil {
		t.Fatal("expected unregistered action to be rejected")
	}
}

func TestRegistryRejectsMissingRequiredParam(t *testing.T) {
	r := testRegistry()
	if err := r.Validate(Action{Name: "move", Parameters: map[string]any{}}); err == nil {
		t.Fatal("expected missing required parameter to be rejected")
	}
}

func TestRegistryRejectsWrongType(t *testing.T) {
	r := testRegistry()
	err := r.Validate(Action{Name: "move", Parameters: map[string]any{"direction": 123}})
	if err == nil {
		t.Fatal("expected wrong parameter type to be rejected")
	}
}

func TestRegistryRejectsUnknownParam(t *testing.T) {
	r := testRegistry()
	err := r.Validate(Action{Name: "move", Parameters: map[string]any{"direction": "up", "danger": true}})
	if err == nil {
		t.Fatal("expected unknown parameter to be rejected")
	}
}

func TestRegistryAcceptsValidAction(t *testing.T) {
	r := testRegistry()
	// JSON numbers decode to float64; an int-typed param must accept whole floats.
	if err := r.Validate(Action{Name: "move", Parameters: map[string]any{"direction": "up", "steps": float64(2)}}); err != nil {
		t.Fatalf("expected valid action, got %v", err)
	}
}
