-- +goose Up

-- Text remains in session_events. This table adds one durable ordering across
-- the lead and reviewer conversations without duplicating the transcript.
CREATE TABLE planning_messages (
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    session_event_id TEXT NOT NULL UNIQUE,
    linked_at TEXT NOT NULL CHECK (length(linked_at) > 0),
    PRIMARY KEY (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (session_event_id) REFERENCES session_events(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_planning_messages_run_sequence
    ON planning_messages(run_id, sequence);
