-- +goose NO TRANSACTION
-- +goose Up

PRAGMA foreign_keys = OFF;

CREATE TABLE sessions_new (
    id TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL,
    agent_id TEXT NOT NULL CHECK (length(trim(agent_id)) > 0),
    role TEXT NOT NULL CHECK (length(role) > 0),
    status TEXT NOT NULL CHECK (
        status IN (
            'starting', 'running', 'waiting_for_user',
            'pause_requested', 'paused', 'completed', 'stopped', 'failed'
        )
    ),
    provider_session_id TEXT NOT NULL,
    started_at TEXT NOT NULL CHECK (length(started_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    ended_at TEXT,
    outcome TEXT NOT NULL DEFAULT '',
    disposition TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    recovery_attempt INTEGER NOT NULL DEFAULT 0 CHECK (recovery_attempt >= 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
) STRICT;

INSERT INTO sessions_new (
    id, run_id, agent_id, role, status, provider_session_id,
    started_at, updated_at, ended_at,
    outcome, disposition, summary, recovery_attempt
)
SELECT
    id, run_id, agent_id, role, status, provider_session_id,
    started_at, updated_at, ended_at,
    outcome, disposition, summary, recovery_attempt
FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX idx_sessions_run_started
    ON sessions(run_id, started_at);

PRAGMA foreign_keys = ON;
