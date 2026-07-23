// Package evaluation implements the Evaluator that gates candidate principles
// and skills. Nothing becomes "active" without passing here first.
package evaluation

import (
	"context"
	"fmt"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// Evaluator applies configured thresholds to principles and validates skills
// against the action registry.
type Evaluator struct {
	cfg      config.EvolutionConfig
	registry *domain.ActionRegistry
}

// New builds an Evaluator.
func New(cfg config.EvolutionConfig, registry *domain.ActionRegistry) *Evaluator {
	return &Evaluator{cfg: cfg, registry: registry}
}

// EvaluatePrinciple checks a principle against evidence/confidence/success-rate
// thresholds. A single experience can never satisfy MinEvidence, so principles
// cannot activate from one episode.
func (e *Evaluator) EvaluatePrinciple(_ context.Context, p domain.Principle) domain.EvalResult {
	details := map[string]any{
		"evidence":     p.Evidence,
		"confidence":   p.Confidence,
		"success_rate": p.SuccessRate,
	}
	if p.Evidence < e.cfg.MinEvidence {
		return fail(fmt.Sprintf("insufficient evidence: %d < %d", p.Evidence, e.cfg.MinEvidence), details)
	}
	if p.Confidence < e.cfg.MinConfidence {
		return fail(fmt.Sprintf("confidence too low: %.2f < %.2f", p.Confidence, e.cfg.MinConfidence), details)
	}
	if e.cfg.MinSuccessRate > 0 && p.SuccessRate < e.cfg.MinSuccessRate {
		return fail(fmt.Sprintf("success rate too low: %.2f < %.2f", p.SuccessRate, e.cfg.MinSuccessRate), details)
	}
	score := p.Confidence + float64(p.Evidence)*0.1 + p.SuccessRate
	return domain.EvalResult{Passed: true, Score: score, Reason: "meets activation thresholds", Details: details}
}

// EvaluateSkill validates that a skill is safe and well-formed: it must have at
// least one step and every step must reference a registered action with valid
// parameters. This is what guarantees "skills can only call registered actions,
// never arbitrary code".
func (e *Evaluator) EvaluateSkill(_ context.Context, sk domain.Skill) domain.EvalResult {
	details := map[string]any{"steps": len(sk.Steps)}
	if len(sk.Steps) == 0 {
		return fail("skill has no steps", details)
	}
	for i, step := range sk.Steps {
		if !e.registry.Has(step.Action) {
			return fail(fmt.Sprintf("step %d references unregistered action %q", i, step.Action), details)
		}
		act := domain.Action{Name: step.Action, Parameters: step.Parameters}
		if err := e.registry.Validate(act); err != nil {
			return fail(fmt.Sprintf("step %d invalid: %v", i, err), details)
		}
	}
	if e.cfg.MinConfidence > 0 && sk.Confidence < e.cfg.MinConfidence {
		return fail(fmt.Sprintf("confidence too low: %.2f < %.2f", sk.Confidence, e.cfg.MinConfidence), details)
	}
	return domain.EvalResult{Passed: true, Score: sk.Confidence + 1, Reason: "all steps reference registered actions", Details: details}
}

func fail(reason string, details map[string]any) domain.EvalResult {
	return domain.EvalResult{Passed: false, Score: 0, Reason: reason, Details: details}
}
