package llm

import (
	"context"
	"testing"

	"liveagent/internal/domain"
)

// Criterion 9: the FakeLLM runs fully offline and returns valid, parseable JSON
// for each cognitive task.
func TestFakeLLMReflectionIsValidJSON(t *testing.T) {
	f := NewFakeLLM()
	user := BuildUserMessage("reflect", map[string]any{
		"task": "reflect",
		"episode": map[string]any{
			"success": false, "energy_low": true, "actions": []any{"explore"},
		},
		"body": map[string]any{"energy": 5.0},
	})
	resp, err := f.Generate(context.Background(), domain.LLMRequest{User: user, JSONMode: true})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := ExtractJSONObject(resp.Text)
	if err != nil {
		t.Fatalf("fake reflection is not valid JSON: %v (%q)", err, resp.Text)
	}
	if !contains(obj, "principle") {
		t.Fatalf("reflection JSON missing principle: %s", obj)
	}
}

func TestFakeLLMGoalPrefersRecoveryWhenEnergyLow(t *testing.T) {
	f := NewFakeLLM()
	user := BuildUserMessage("goal", map[string]any{
		"task": "goal",
		"body": map[string]any{"energy": 5.0},
		"goals": []any{
			map[string]any{"id": "explore_world", "tags": []any{"explore"}},
			map[string]any{"id": "stay_alive", "tags": []any{"recover"}},
		},
	})
	resp, _ := f.Generate(context.Background(), domain.LLMRequest{User: user})
	if !contains(resp.Text, "stay_alive") {
		t.Fatalf("expected recovery goal when energy low, got %s", resp.Text)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
