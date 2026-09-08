-- +goose Up

CREATE TABLE worker_attempts (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('start', 'resume')),
    assignment_json TEXT NOT NULL CHECK (json_valid(assignment_json)),
    launch_idempotency_key TEXT NOT NULL,
    launch_request_digest TEXT NOT NULL,
    provider_session_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (
        state IN (
            'starting', 'running', 'pause_requested', 'paused',
            'stop_requested', 'terminal', 'indeterminate'
        )
    ),
    latest_event_sequence INTEGER NOT NULL DEFAULT 0
        CHECK (latest_event_sequence >= 0),
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    ended_at TEXT,
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    PRIMARY KEY (session_id, attempt_id)
) STRICT;

CREATE UNIQUE INDEX idx_worker_attempts_one_nonterminal_per_session
    ON worker_attempts(session_id)
    WHERE state <> 'terminal';

CREATE TABLE worker_mutations (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (
        kind IN ('message', 'pause', 'continue', 'stop', 'force_stop')
    ),
    request_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'applied', 'rejected', 'indeterminate')
    ),
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, attempt_id, idempotency_key),
    FOREIGN KEY (session_id, attempt_id)
        REFERENCES worker_attempts(session_id, attempt_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE worker_events (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    event_json TEXT NOT NULL CHECK (json_valid(event_json)),
    PRIMARY KEY (session_id, attempt_id, sequence),
    FOREIGN KEY (session_id, attempt_id)
        REFERENCES worker_attempts(session_id, attempt_id) ON DELETE RESTRICT
) STRICT;
