-- +goose Up

CREATE TABLE worker_attempt_checkpoints (
    session_id TEXT NOT NULL PRIMARY KEY,
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    last_event_sequence INTEGER NOT NULL DEFAULT 0
        CHECK (last_event_sequence >= 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE RESTRICT
) STRICT;

ALTER TABLE session_events ADD COLUMN worker_attempt_id TEXT;
ALTER TABLE session_events ADD COLUMN worker_event_sequence INTEGER
    CHECK (
        (worker_attempt_id IS NULL AND worker_event_sequence IS NULL)
        OR
        (
            worker_attempt_id IS NOT NULL
            AND length(trim(worker_attempt_id)) > 0
            AND worker_event_sequence IS NOT NULL
            AND worker_event_sequence > 0
        )
    );

CREATE UNIQUE INDEX idx_session_events_worker_source
    ON session_events(session_id, worker_attempt_id, worker_event_sequence)
    WHERE worker_attempt_id IS NOT NULL;
