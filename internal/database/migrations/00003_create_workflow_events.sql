-- +goose Up

CREATE TABLE workflow_events (
    id TEXT NOT NULL PRIMARY KEY,
    aggregate_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (length(event_type) > 0),
    actor_kind TEXT NOT NULL CHECK (length(actor_kind) > 0),
    actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    payload_version INTEGER NOT NULL CHECK (payload_version > 0),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) > 0),
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    FOREIGN KEY (aggregate_id) REFERENCES features(id) ON DELETE RESTRICT,
    UNIQUE (aggregate_id, sequence),
    UNIQUE (aggregate_id, idempotency_key)
) STRICT;

CREATE INDEX idx_workflow_events_aggregate_sequence
    ON workflow_events(aggregate_id, sequence);
