// Package store is the agent's persistence layer: a small SQLite database that
// records episodes (one per task run), the events within them (every tool call
// and its result), and free-form notes the agent may keep for later. It is
// intentionally minimal — the elaborate learning tables come later, once a
// verified learning loop exists.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite connection.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS episodes (
    id         TEXT PRIMARY KEY,
    agent      TEXT NOT NULL,
    task       TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at   TEXT,
    steps      INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT 'running',
    summary    TEXT
);
CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    episode_id TEXT NOT NULL,
    step       INTEGER NOT NULL,
    ts         TEXT NOT NULL,
    tool       TEXT NOT NULL,
    args       TEXT,
    result     TEXT,
    is_error   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_episode ON events(episode_id);
CREATE TABLE IF NOT EXISTS notes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    agent        TEXT NOT NULL,
    key          TEXT,
    text         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    uses         INTEGER NOT NULL DEFAULT 0,
    wins         INTEGER NOT NULL DEFAULT 0,
    last_used_at TEXT
);
`

// migrations are idempotent ALTER statements applied after the base schema so
// databases created by older versions gain the newer note-stats columns. Errors
// (e.g. "duplicate column name") are ignored on purpose.
var migrations = []string{
	`ALTER TABLE notes ADD COLUMN uses INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE notes ADD COLUMN wins INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE notes ADD COLUMN last_used_at TEXT`,
	`ALTER TABLE notes ADD COLUMN embedding TEXT`, // JSON array of float32 for semantic recall
}

// Open opens (creating if needed) the SQLite database and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // modernc sqlite: serialise writes
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: init schema: %w", err)
	}
	for _, m := range migrations {
		_, _ = db.Exec(m) // ignore "duplicate column" on already-migrated DBs
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// StartEpisode records a new task attempt and returns its id.
func (s *Store) StartEpisode(ctx context.Context, agent, task string) (string, error) {
	id := fmt.Sprintf("ep_%d", time.Now().UnixNano())
	if err := s.StartEpisodeID(ctx, id, agent, task); err != nil {
		return "", err
	}
	return id, nil
}

// StartEpisodeID records a new episode under a caller-chosen id. The web layer
// uses this so one id identifies the whole session (workspace dir, episode,
// event stream) even across many conversational turns.
func (s *Store) StartEpisodeID(ctx context.Context, id, agent, task string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO episodes(id, agent, task, started_at) VALUES(?,?,?,?)`,
		id, agent, task, nowUTC())
	if err != nil {
		return fmt.Errorf("store: start episode: %w", err)
	}
	return nil
}

// RecordEvent appends one tool call + result to an episode. args is marshalled
// to JSON for storage.
func (s *Store) RecordEvent(ctx context.Context, episodeID string, step int, tool string, args map[string]any, result string, isError bool) error {
	argsJSON := ""
	if len(args) > 0 {
		if b, err := json.Marshal(args); err == nil {
			argsJSON = string(b)
		}
	}
	ie := 0
	if isError {
		ie = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events(episode_id, step, ts, tool, args, result, is_error) VALUES(?,?,?,?,?,?,?)`,
		episodeID, step, nowUTC(), tool, argsJSON, result, ie)
	if err != nil {
		return fmt.Errorf("store: record event: %w", err)
	}
	return nil
}

// InterruptOrphans marks any episode still flagged "running" as "interrupted".
// Turns are serialised in-memory, so at process start none can legitimately be
// running; such rows are leftovers from a crash or restart mid-turn. Returns
// how many rows were fixed.
func (s *Store) InterruptOrphans(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE episodes SET status='interrupted', ended_at=? WHERE status='running'`, nowUTC())
	if err != nil {
		return 0, fmt.Errorf("store: interrupt orphans: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// EndEpisode finalises an episode with its outcome.
func (s *Store) EndEpisode(ctx context.Context, episodeID, status, summary string, steps int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE episodes SET ended_at=?, status=?, summary=?, steps=? WHERE id=?`,
		nowUTC(), status, summary, steps, episodeID)
	if err != nil {
		return fmt.Errorf("store: end episode: %w", err)
	}
	return nil
}

// EpisodeRow is a summary of one past run, for listing in a UI.
type EpisodeRow struct {
	ID        string `json:"id"`
	Task      string `json:"task"`
	Status    string `json:"status"`
	Steps     int    `json:"steps"`
	StartedAt string `json:"started_at"`
	Summary   string `json:"summary"`
}

// ListEpisodes returns recent episodes for an agent, newest first.
func (s *Store) ListEpisodes(ctx context.Context, agent string, limit int) ([]EpisodeRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task, status, steps, started_at, COALESCE(summary,'')
		   FROM episodes WHERE agent=? ORDER BY started_at DESC LIMIT ?`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EpisodeRow
	for rows.Next() {
		var e EpisodeRow
		if err := rows.Scan(&e.ID, &e.Task, &e.Status, &e.Steps, &e.StartedAt, &e.Summary); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventRow is one recorded tool call + result within an episode.
type EventRow struct {
	Step    int    `json:"step"`
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}

// EpisodeEvents returns the ordered events of one episode.
func (s *Store) EpisodeEvents(ctx context.Context, episodeID string) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT step, tool, COALESCE(args,''), COALESCE(result,''), is_error
		   FROM events WHERE episode_id=? ORDER BY id ASC`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var ie int
		if err := rows.Scan(&e.Step, &e.Tool, &e.Args, &e.Result, &ie); err != nil {
			return nil, err
		}
		e.IsError = ie != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// EpisodeMeta returns one episode's summary row by id.
func (s *Store) EpisodeMeta(ctx context.Context, id string) (EpisodeRow, error) {
	var e EpisodeRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id, task, status, steps, started_at, COALESCE(summary,'')
		   FROM episodes WHERE id=?`, id).
		Scan(&e.ID, &e.Task, &e.Status, &e.Steps, &e.StartedAt, &e.Summary)
	return e, err
}

// SaveNote persists a lesson and returns its row id.
func (s *Store) SaveNote(ctx context.Context, agent, key, text string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notes(agent, key, text, created_at) VALUES(?,?,?,?)`,
		agent, key, text, nowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Note is a stored lesson plus its reinforcement stats. Uses counts how often
// it was recalled into a run; Wins counts how often such a run ended in a
// verified success.
type Note struct {
	ID   int64
	Key  string
	Text string
	Uses int
	Wins int
}

// ListNotes returns notes for an agent, newest first.
func (s *Store) ListNotes(ctx context.Context, agent string, limit int) ([]Note, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, text, uses, wins FROM notes WHERE agent=? ORDER BY id DESC LIMIT ?`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Key, &n.Text, &n.Uses, &n.Wins); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNotesUsed increments the use counter (and last_used_at) for the given
// note ids — called when lessons are recalled into a run.
func (s *Store) MarkNotesUsed(ctx context.Context, ids []int64) error {
	return s.bump(ctx, "uses", ids, true)
}

// MarkNotesWin increments the win counter for the given note ids — called when
// a run that used them ended in a verified success.
func (s *Store) MarkNotesWin(ctx context.Context, ids []int64) error {
	return s.bump(ctx, "wins", ids, false)
}

func (s *Store) bump(ctx context.Context, col string, ids []int64, touch bool) error {
	if len(ids) == 0 {
		return nil
	}
	set := col + "=" + col + "+1"
	if touch {
		set += ", last_used_at='" + nowUTC() + "'"
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE notes SET `+set+` WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ReinforceNote bumps a note's win counter when the same lesson is independently
// re-derived from another verified success (a strong confirmation), instead of
// storing a near-duplicate.
func (s *Store) ReinforceNote(ctx context.Context, id int64) error {
	return s.MarkNotesWin(ctx, []int64{id})
}

// SetNoteEmbedding stores a note's embedding vector (as a JSON float array).
func (s *Store) SetNoteEmbedding(ctx context.Context, id int64, vec []float32) error {
	b, err := json.Marshal(vec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE notes SET embedding=? WHERE id=?`, string(b), id)
	return err
}

// NoteEmbeddings returns the stored embedding vectors for an agent's notes,
// keyed by note id. Notes without an embedding are omitted.
func (s *Store) NoteEmbeddings(ctx context.Context, agent string) (map[int64][]float32, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, embedding FROM notes WHERE agent=? AND embedding IS NOT NULL AND embedding<>''`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]float32{}
	for rows.Next() {
		var id int64
		var js string
		if err := rows.Scan(&id, &js); err != nil {
			return nil, err
		}
		var vec []float32
		if json.Unmarshal([]byte(js), &vec) == nil && len(vec) > 0 {
			out[id] = vec
		}
	}
	return out, rows.Err()
}

// EvictWeak deletes lessons that have been surfaced at least minUses times yet
// never contributed to a verified win (wins=0). A minUses of 0 disables it.
func (s *Store) EvictWeak(ctx context.Context, agent string, minUses int) (int64, error) {
	if minUses <= 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM notes WHERE agent=? AND wins=0 AND uses>=?`, agent, minUses)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
