// Package evolution manages the lifecycle of learned knowledge:
//
//	candidate -> evaluated -> active -> deprecated
//
// It merges duplicate principles (accumulating evidence, success rate and
// confidence) instead of endlessly appending, and it only activates a principle
// or skill after the Evaluator passes it. It never modifies root goals, reward
// weights or safety constraints.
package evolution

import (
	"context"
	"strings"
	"time"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/reflection"
)

// Repository is the persistence surface evolution needs. *memory.Store satisfies it.
type Repository interface {
	ListPrinciples(ctx context.Context, status string) ([]domain.Principle, error)
	ListSkills(ctx context.Context, status string) ([]domain.Skill, error)
	SavePrinciple(ctx context.Context, p domain.Principle) error
	SaveSkill(ctx context.Context, sk domain.Skill) error
	RecordEvaluation(ctx context.Context, agentID, kind, id string, res domain.EvalResult) error
}

// Manager applies reflections to the knowledge base.
type Manager struct {
	repo      Repository
	evaluator domain.Evaluator
	cfg       config.EvolutionConfig
}

// New builds a Manager.
func New(repo Repository, evaluator domain.Evaluator, cfg config.EvolutionConfig) *Manager {
	return &Manager{repo: repo, evaluator: evaluator, cfg: cfg}
}

// Result summarises what a reflection produced, for logging/audit.
type Result struct {
	PrincipleID     string
	PrincipleStatus string
	SkillID         string
	SkillStatus     string
}

// ProcessReflection converts a parsed reflection into knowledge updates. A
// reflection that failed to parse (Parsed=false) is ignored here — the caller
// keeps the raw text but must not create principles from it.
func (m *Manager) ProcessReflection(ctx context.Context, agentID string, ref reflection.Reflection, episodeSuccess bool) (Result, error) {
	var res Result
	if !ref.Parsed || strings.TrimSpace(ref.Principle) == "" {
		return res, nil
	}

	p, err := m.upsertPrinciple(ctx, agentID, ref, episodeSuccess)
	if err != nil {
		return res, err
	}
	res.PrincipleID = p.ID
	res.PrincipleStatus = p.Status

	if ref.SuggestedSkill != nil && len(ref.SuggestedSkill.Steps) > 0 {
		sk, err := m.proposeSkill(ctx, agentID, ref)
		if err != nil {
			return res, err
		}
		res.SkillID = sk.ID
		res.SkillStatus = sk.Status
	}
	return res, nil
}

// upsertPrinciple merges into an existing principle (same normalised text) or
// creates a new candidate, then re-evaluates it for possible activation.
func (m *Manager) upsertPrinciple(ctx context.Context, agentID string, ref reflection.Reflection, episodeSuccess bool) (domain.Principle, error) {
	existing, err := m.repo.ListPrinciples(ctx, "")
	if err != nil {
		return domain.Principle{}, err
	}
	norm := normalize(ref.Principle)

	var p domain.Principle
	found := false
	for _, e := range existing {
		if normalize(e.Text) == norm {
			p = e
			found = true
			break
		}
	}

	successInc := 0.0
	if episodeSuccess {
		successInc = 1.0
	}

	if found {
		// Merge evidence, running success rate and confidence.
		newEvidence := p.Evidence + 1
		p.SuccessRate = (p.SuccessRate*float64(p.Evidence) + successInc) / float64(newEvidence)
		p.Evidence = newEvidence
		// Confidence trends toward the reported value but rewards corroboration.
		p.Confidence = clamp01((p.Confidence+ref.Confidence)/2.0 + 0.05)
		p.ApplicableConditions = union(p.ApplicableConditions, ref.ApplicableConditions)
	} else {
		p = domain.Principle{
			ID:                   domain.NewID("prin"),
			AgentID:              agentID,
			Text:                 ref.Principle,
			ApplicableConditions: ref.ApplicableConditions,
			Confidence:           clamp01(ref.Confidence),
			Status:               domain.StatusCandidate,
			Evidence:             1,
			SuccessRate:          successInc,
			Tags:                 ref.ApplicableConditions, // conditions double as tags for retrieval
			CreatedAt:            time.Now().UTC(),
		}
	}

	// Evaluate for activation. A single episode can never reach MinEvidence, so
	// principles cannot self-activate from one experience.
	if p.Status != domain.StatusActive {
		p.Status = domain.StatusEvaluated
		eval := m.evaluator.EvaluatePrinciple(ctx, p)
		_ = m.repo.RecordEvaluation(ctx, agentID, domain.KindPrinciple, p.ID, eval)
		if eval.Passed {
			p.Status = domain.StatusActive
		} else {
			p.Status = domain.StatusCandidate
		}
	}
	if err := m.repo.SavePrinciple(ctx, p); err != nil {
		return domain.Principle{}, err
	}
	return p, nil
}

// proposeSkill builds a candidate skill from the reflection and evaluates it.
// Skills only activate if every step references a registered action.
func (m *Manager) proposeSkill(ctx context.Context, agentID string, ref reflection.Reflection) (domain.Skill, error) {
	// Deduplicate by name: update an existing skill instead of appending a new
	// row every episode.
	sk := domain.Skill{
		ID:         domain.NewID("skill"),
		AgentID:    agentID,
		Name:       ref.SuggestedSkill.Name,
		Steps:      ref.SuggestedSkill.Steps,
		Status:     domain.StatusCandidate,
		Confidence: clamp01(ref.Confidence),
		Tags:       ref.ApplicableConditions,
		CreatedAt:  time.Now().UTC(),
	}
	if existing, err := m.repo.ListSkills(ctx, ""); err == nil {
		for _, e := range existing {
			if e.Name == sk.Name {
				sk.ID = e.ID
				sk.CreatedAt = e.CreatedAt
				sk.Confidence = clamp01((e.Confidence + ref.Confidence) / 2.0)
				break
			}
		}
	}
	eval := m.evaluator.EvaluateSkill(ctx, sk)
	_ = m.repo.RecordEvaluation(ctx, agentID, domain.KindSkill, sk.ID, eval)
	if eval.Passed {
		sk.Status = domain.StatusActive
	}
	if err := m.repo.SaveSkill(ctx, sk); err != nil {
		return domain.Skill{}, err
	}
	return sk, nil
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func union(a, b []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}
