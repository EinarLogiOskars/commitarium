-- +goose Up

CREATE TABLE run_interventions (
    id TEXT NOT NULL PRIMARY KEY,
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    target_role TEXT NOT NULL CHECK (target_role IN ('lead', 'reviewer')),
    message TEXT NOT NULL CHECK (length(trim(message)) > 0),
    status TEXT NOT NULL CHECK (
        status IN ('waiting_for_boundary', 'queued', 'being_answered', 'answered')
    ),
    requested_at TEXT NOT NULL CHECK (length(requested_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    answered_at TEXT,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_run_interventions_run_requested
    ON run_interventions(run_id, requested_at DESC, id DESC);

CREATE UNIQUE INDEX idx_run_interventions_one_unfinished
    ON run_interventions(run_id)
    WHERE status != 'answered';
