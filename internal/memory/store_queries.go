package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"liveagent/internal/domain"
)

// sortByScore sorts memory items by descending score (stable).
func sortByScore(items []domain.MemoryItem) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
}

// -----------------------------------------------------------------------------
// Agent identity & state
// -----------------------------------------------------------------------------

// LoadAgentByName returns a persisted agent state for the given name, or false
// if none exists. This is how identity/age/memory survive restarts.
func (s *Store) LoadAgentByName(ctx context.Context, name string) (domain.AgentState, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT state_json FROM agents WHERE name=? LIMIT 1`, name)
	var stateJSON string
	if err := row.Scan(&stateJSON); err != nil {
		if err == sql.ErrNoRows {
			return domain.AgentState{}, false, nil
		}
		return domain.AgentState{}, false, err
	}
	var st domain.AgentState
	if err := json.Unmarshal([]byte(stateJSON), &st); err != nil {
		return domain.AgentState{}, false, err
	}
	return st, true, nil
}

// SaveAgentState upserts the full agent state (identity + learned state).
func (s *Store) SaveAgentState(ctx context.Context, name string, st domain.AgentState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, name, created_at, age, state_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   name=excluded.name, age=excluded.age, state_json=excluded.state_json, updated_at=excluded.updated_at`,
		st.AgentID, name, st.CreatedAt.Format(time.RFC3339Nano), st.Age, encode(st), nowStr(),
	)
	return err
}

// -----------------------------------------------------------------------------
// Events (append-only audit log)
// -----------------------------------------------------------------------------

// RecordEvent appends an audit event.
func (s *Store) RecordEvent(ctx context.Context, agentID string, tick int64, kind string, payload any) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (agent_id, tick, kind, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		agentID, tick, kind, encode(payload), nowStr())
	return err
}

// -----------------------------------------------------------------------------
// Episodes
// -----------------------------------------------------------------------------

// SaveEpisode persists a full episode trajectory.
func (s *Store) SaveEpisode(ctx context.Context, ep domain.Episode) error {
	if ep.ID == "" {
		ep.ID = domain.NewID("epi")
	}
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO episodes (id, agent_id, goal, start_tick, end_tick, success, total_reward, summary, tags, data_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ep.ID, ep.AgentID, ep.Goal, ep.StartTick, ep.EndTick, boolToInt(ep.Success),
		ep.TotalReward, ep.Summary, joinTags(ep.Tags), encode(ep), ep.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) listEpisodes(ctx context.Context, limit int) ([]domain.Episode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data_json FROM episodes ORDER BY end_tick DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Episode
	for rows.Next() {
		var dj string
		if err := rows.Scan(&dj); err != nil {
			return nil, err
		}
		var ep domain.Episode
		if err := json.Unmarshal([]byte(dj), &ep); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// CountExperiences returns the number of stored experiences for an agent.
func (s *Store) CountExperiences(ctx context.Context, agentID string) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiences WHERE agent_id=?`, agentID)
	var n int
	err := row.Scan(&n)
	return n, err
}

// -----------------------------------------------------------------------------
// Principles & skills listing
// -----------------------------------------------------------------------------

// ListPrinciples returns principles, optionally filtered by status ("" = all).
func (s *Store) ListPrinciples(ctx context.Context, status string) ([]domain.Principle, error) {
	return s.listPrinciples(ctx, status)
}

func (s *Store) listPrinciples(ctx context.Context, status string) ([]domain.Principle, error) {
	q := `SELECT id, agent_id, text, conditions, confidence, status, evidence, success_rate, tags, created_at, updated_at FROM principles`
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.QueryContext(ctx, q)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE status=?`, status)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Principle
	for rows.Next() {
		var p domain.Principle
		var conds, tags, created, updated string
		if err := rows.Scan(&p.ID, &p.AgentID, &p.Text, &conds, &p.Confidence, &p.Status,
			&p.Evidence, &p.SuccessRate, &tags, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(conds), &p.ApplicableConditions)
		p.Tags = splitTags(tags)
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListSkills returns skills, optionally filtered by status ("" = all).
func (s *Store) ListSkills(ctx context.Context, status string) ([]domain.Skill, error) {
	return s.listSkills(ctx, status)
}

func (s *Store) listSkills(ctx context.Context, status string) ([]domain.Skill, error) {
	q := `SELECT data_json FROM skills`
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.QueryContext(ctx, q)
	} else {
		rows, err = s.db.QueryContext(ctx, q+` WHERE status=?`, status)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Skill
	for rows.Next() {
		var dj string
		if err := rows.Scan(&dj); err != nil {
			return nil, err
		}
		var sk domain.Skill
		if err := json.Unmarshal([]byte(dj), &sk); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Evaluation runs & policy versions (audit)
// -----------------------------------------------------------------------------

// RecordEvaluation stores an evaluation run for audit/rollback.
func (s *Store) RecordEvaluation(ctx context.Context, agentID, targetKind, targetID string, res domain.EvalResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO evaluation_runs (agent_id, target_kind, target_id, passed, score, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		agentID, targetKind, targetID, boolToInt(res.Passed), res.Score, res.Reason, nowStr())
	return err
}

// RecordPolicyVersion appends a policy version entry.
func (s *Store) RecordPolicyVersion(ctx context.Context, agentID, version, description string, active bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO policy_versions (agent_id, version, description, active, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		agentID, version, description, boolToInt(active), nowStr())
	return err
}
