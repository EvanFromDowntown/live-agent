// Package memory implements the SQLite-backed Memory. Raw experiences and events
// are append-only; principles/skills/state are upserted with full audit history
// via the events and evaluation_runs tables.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"liveagent/internal/domain"
)

// Store is the concrete Memory implementation. It satisfies domain.Memory and
// exposes additional persistence used by the kernel (state, episodes, audit).
type Store struct {
	db     *sql.DB
	scorer Scorer
}

// Open opens (or creates) the SQLite database and ensures the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("memory: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // SQLite: serialise writes to avoid lock churn
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("memory: init schema: %w", err)
	}
	return &Store{db: db, scorer: DefaultScorer{}}, nil
}

// SetScorer replaces the retrieval scorer (e.g. to plug in embeddings later).
func (s *Store) SetScorer(sc Scorer) { s.scorer = sc }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func encode(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func joinTags(tags []string) string { return strings.Join(tags, ",") }

func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// -----------------------------------------------------------------------------
// domain.Memory implementation
// -----------------------------------------------------------------------------

// AppendExperience appends a raw experience (append-only).
func (s *Store) AppendExperience(ctx context.Context, exp domain.Experience) error {
	if exp.ID == "" {
		exp.ID = domain.NewID("exp")
	}
	if exp.CreatedAt.IsZero() {
		exp.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO experiences (id, agent_id, episode_id, tick, goal, action, reward, success, tags, data_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		exp.ID, exp.AgentID, exp.EpisodeID, exp.Tick, exp.Goal, exp.Action.Name,
		exp.Reward.TaskSuccess, boolToInt(exp.Outcome.Success), joinTags(exp.Tags),
		encode(exp), exp.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("memory: append experience: %w", err)
	}
	return nil
}

// SavePrinciple upserts a principle by ID.
func (s *Store) SavePrinciple(ctx context.Context, p domain.Principle) error {
	if p.ID == "" {
		p.ID = domain.NewID("prin")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO principles (id, agent_id, text, conditions, confidence, status, evidence, success_rate, tags, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   text=excluded.text, conditions=excluded.conditions, confidence=excluded.confidence,
		   status=excluded.status, evidence=excluded.evidence, success_rate=excluded.success_rate,
		   tags=excluded.tags, updated_at=excluded.updated_at`,
		p.ID, p.AgentID, p.Text, encode(p.ApplicableConditions), p.Confidence, p.Status,
		p.Evidence, p.SuccessRate, joinTags(p.Tags),
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("memory: save principle: %w", err)
	}
	return nil
}

// SaveSkill upserts a skill by ID.
func (s *Store) SaveSkill(ctx context.Context, sk domain.Skill) error {
	if sk.ID == "" {
		sk.ID = domain.NewID("skill")
	}
	if sk.CreatedAt.IsZero() {
		sk.CreatedAt = time.Now().UTC()
	}
	sk.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO skills (id, agent_id, name, data_json, status, confidence, tags, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, data_json=excluded.data_json, status=excluded.status,
		   confidence=excluded.confidence, tags=excluded.tags, updated_at=excluded.updated_at`,
		sk.ID, sk.AgentID, sk.Name, encode(sk), sk.Status, sk.Confidence, joinTags(sk.Tags),
		sk.CreatedAt.Format(time.RFC3339Nano), sk.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("memory: save skill: %w", err)
	}
	return nil
}

// Consolidate is a hook for memory maintenance (dedup/summarise). For the MVP it
// deprecates principles whose confidence has decayed to ~zero. It is safe to
// call frequently and is a no-op when nothing needs doing.
func (s *Store) Consolidate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE principles SET status=? , updated_at=? WHERE status=? AND confidence < 0.05`,
		domain.StatusDeprecated, nowStr(), domain.StatusActive)
	return err
}

// Retrieve scores stored items against the query and returns the top results.
func (s *Store) Retrieve(ctx context.Context, q domain.MemoryQuery) ([]domain.MemoryItem, error) {
	if q.Now.IsZero() {
		q.Now = time.Now().UTC()
	}
	kinds := q.Kinds
	if len(kinds) == 0 {
		kinds = []string{domain.KindPrinciple, domain.KindSkill, domain.KindEpisode}
	}
	var items []domain.MemoryItem
	for _, kind := range kinds {
		switch kind {
		case domain.KindPrinciple:
			ps, err := s.listPrinciples(ctx, "")
			if err != nil {
				return nil, err
			}
			for i := range ps {
				p := ps[i]
				items = append(items, domain.MemoryItem{
					Kind: domain.KindPrinciple, ID: p.ID, Text: p.Text, Principle: &p,
				})
			}
		case domain.KindSkill:
			sks, err := s.listSkills(ctx, "")
			if err != nil {
				return nil, err
			}
			for i := range sks {
				sk := sks[i]
				items = append(items, domain.MemoryItem{
					Kind: domain.KindSkill, ID: sk.ID, Text: sk.Name, Skill: &sk,
				})
			}
		case domain.KindEpisode:
			eps, err := s.listEpisodes(ctx, 100)
			if err != nil {
				return nil, err
			}
			for i := range eps {
				ep := eps[i]
				items = append(items, domain.MemoryItem{
					Kind: domain.KindEpisode, ID: ep.ID, Text: ep.Summary, Episode: &ep,
				})
			}
		}
	}
	for i := range items {
		items[i].Score = s.scorer.Score(q, items[i])
	}
	// sort descending by score (simple insertion-free selection via stdlib)
	sortByScore(items)
	limit := q.Limit
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	return items[:limit], nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
