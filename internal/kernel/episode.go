package kernel

import (
	"fmt"

	"liveagent/internal/domain"
)

// summarize produces a short human-readable episode summary used for retrieval
// and audit.
func summarize(ep *domain.Episode, success bool) string {
	verb := "failed"
	if success {
		verb = "succeeded"
	}
	actions := map[string]int{}
	for _, e := range ep.Trace {
		actions[e.Action.Name]++
	}
	return fmt.Sprintf("goal %q %s over %d steps; actions=%v",
		ep.Goal, verb, len(ep.Trace), actions)
}

// episodeTags derives retrieval tags from the episode outcome and end body.
func episodeTags(ep *domain.Episode, success bool, endBody map[string]any) []string {
	tags := []string{}
	if success {
		tags = append(tags, "success", "exploration_task")
	} else {
		tags = append(tags, "failure", "long_horizon_task")
	}
	if e, ok := endBody["energy"].(float64); ok && e < 30 {
		tags = append(tags, "energy_low")
	}
	return tags
}
