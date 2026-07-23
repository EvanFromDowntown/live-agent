package config

import "liveagent/internal/domain"

// Scalarize collapses a multi-dimensional RewardVector into a single scalar
// using the human-defined weights. ResourceCost and RiskCost are treated as
// penalties (subtracted). This is the ONLY place scalarisation happens.
func (w RewardWeights) Scalarize(r domain.RewardVector) float64 {
	return w.TaskSuccess*r.TaskSuccess +
		w.Homeostasis*r.Homeostasis +
		w.InformationGain*r.InformationGain +
		w.HumanFeedback*r.HumanFeedback -
		w.ResourceCost*r.ResourceCost -
		w.RiskCost*r.RiskCost
}
