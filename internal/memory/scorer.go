package memory

import (
	"strings"
	"time"

	"liveagent/internal/domain"
)

// Scorer ranks a memory item for a query. It is deliberately an interface so the
// tag+keyword+recency heuristic below can later be swapped for an embedding-based
// scorer without touching the Store or the Agent.
type Scorer interface {
	Score(q domain.MemoryQuery, item domain.MemoryItem) float64
}

// DefaultScorer implements the MVP retrieval formula:
//
//	tag match + keyword match + time freshness + importance + historical success rate
//
// No vector database required.
type DefaultScorer struct{}

// Score combines the signals into a single relevance score.
func (DefaultScorer) Score(q domain.MemoryQuery, item domain.MemoryItem) float64 {
	tags, text, conf, success, status, updated := extract(item)

	var score float64

	// 1. Tag match.
	score += 2.0 * float64(overlap(q.Tags, tags))

	// 2. Keyword match against the item's text/conditions.
	hay := strings.ToLower(text)
	for _, kw := range q.Keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(kw)) {
			score += 1.0
		}
	}

	// 3. Time freshness (newer is better), gentle decay over ~7 days.
	if !updated.IsZero() {
		ageHours := q.Now.Sub(updated).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		score += 1.0 / (1.0 + ageHours/168.0)
	}

	// 4. Importance = confidence.
	score += conf

	// 5. Historical success rate.
	score += success

	// Prefer active knowledge; strongly demote deprecated.
	switch status {
	case domain.StatusActive:
		score += 1.0
	case domain.StatusDeprecated:
		score -= 5.0
	}

	return score
}

func extract(item domain.MemoryItem) (tags []string, text string, conf, success float64, status string, updated time.Time) {
	switch item.Kind {
	case domain.KindPrinciple:
		if p := item.Principle; p != nil {
			text = p.Text + " " + strings.Join(p.ApplicableConditions, " ")
			return p.Tags, text, p.Confidence, p.SuccessRate, p.Status, p.UpdatedAt
		}
	case domain.KindSkill:
		if sk := item.Skill; sk != nil {
			text = sk.Name + " " + strings.Join(sk.Preconditions, " ")
			return sk.Tags, text, sk.Confidence, 0, sk.Status, sk.UpdatedAt
		}
	case domain.KindEpisode:
		if ep := item.Episode; ep != nil {
			var sr float64
			if ep.Success {
				sr = 1
			}
			return ep.Tags, ep.Summary, 0, sr, "", ep.CreatedAt
		}
	}
	return nil, item.Text, 0, 0, "", time.Time{}
}

func overlap(a, b []string) int {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	n := 0
	for _, y := range b {
		if _, ok := set[y]; ok {
			n++
		}
	}
	return n
}
