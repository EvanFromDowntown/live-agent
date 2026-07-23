package memory

// schema is the full SQLite DDL. All tables are created if absent so a fresh DB
// bootstraps automatically and an existing DB is reused on restart.
const schema = `
CREATE TABLE IF NOT EXISTS agents (
    agent_id   TEXT PRIMARY KEY,
    name       TEXT,
    created_at TEXT,
    age        INTEGER,
    state_json TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT,
    tick       INTEGER,
    kind       TEXT,
    payload    TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS experiences (
    id         TEXT PRIMARY KEY,
    agent_id   TEXT,
    episode_id TEXT,
    tick       INTEGER,
    goal       TEXT,
    action     TEXT,
    reward     REAL,
    success    INTEGER,
    tags       TEXT,
    data_json  TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS episodes (
    id           TEXT PRIMARY KEY,
    agent_id     TEXT,
    goal         TEXT,
    start_tick   INTEGER,
    end_tick     INTEGER,
    success      INTEGER,
    total_reward REAL,
    summary      TEXT,
    tags         TEXT,
    data_json    TEXT,
    created_at   TEXT
);

CREATE TABLE IF NOT EXISTS principles (
    id           TEXT PRIMARY KEY,
    agent_id     TEXT,
    text         TEXT,
    conditions   TEXT,
    confidence   REAL,
    status       TEXT,
    evidence     INTEGER,
    success_rate REAL,
    tags         TEXT,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS skills (
    id           TEXT PRIMARY KEY,
    agent_id     TEXT,
    name         TEXT,
    data_json    TEXT,
    status       TEXT,
    confidence   REAL,
    tags         TEXT,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS policy_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT,
    version     TEXT,
    description TEXT,
    active      INTEGER,
    created_at  TEXT
);

CREATE TABLE IF NOT EXISTS evaluation_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT,
    target_kind TEXT,
    target_id   TEXT,
    passed      INTEGER,
    score       REAL,
    reason      TEXT,
    created_at  TEXT
);

CREATE INDEX IF NOT EXISTS idx_exp_agent   ON experiences(agent_id);
CREATE INDEX IF NOT EXISTS idx_epi_agent   ON episodes(agent_id);
CREATE INDEX IF NOT EXISTS idx_prin_agent  ON principles(agent_id);
CREATE INDEX IF NOT EXISTS idx_skill_agent ON skills(agent_id);
`
