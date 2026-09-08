-- +goose Up

CREATE TABLE runs (
    id TEXT NOT NULL PRIMARY KEY,
    feature_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('running', 'waiting_for_user', 'succeeded', 'stopped', 'failed')
    ),
    reason TEXT NOT NULL,
    started_at TEXT NOT NULL CHECK (length(started_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    ended_at TEXT,
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_runs_feature_started
    ON runs(feature_id, started_at);

CREATE TABLE sessions (
    id TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL,
    agent_id TEXT NOT NULL CHECK (length(trim(agent_id)) > 0),
    role TEXT NOT NULL CHECK (length(role) > 0),
    status TEXT NOT NULL CHECK (
        status IN (
            'starting', 'running', 'pause_requested', 'paused',
            'completed', 'stopped', 'failed'
        )
    ),
    provider_session_id TEXT NOT NULL,
    started_at TEXT NOT NULL CHECK (length(started_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    ended_at TEXT,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_sessions_run_started
    ON sessions(run_id, started_at);

CREATE TABLE session_events (
    id TEXT NOT NULL PRIMARY KEY,
    session_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (length(event_type) > 0),
    text TEXT NOT NULL CHECK (length(trim(text)) > 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE RESTRICT,
    UNIQUE (session_id, sequence)
) STRICT;

CREATE INDEX idx_session_events_session_sequence
    ON session_events(session_id, sequence);

CREATE TABLE session_commands (
    id TEXT NOT NULL PRIMARY KEY,
    session_id TEXT NOT NULL,
    command_type TEXT NOT NULL CHECK (length(command_type) > 0),
    message TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'applied', 'rejected')),
    requested_at TEXT NOT NULL CHECK (length(requested_at) > 0),
    applied_at TEXT,
    error TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_session_commands_session_requested
    ON session_commands(session_id, requested_at);
