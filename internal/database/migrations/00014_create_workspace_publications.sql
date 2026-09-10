-- +goose Up

CREATE TABLE workspace_publications (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    commit_message TEXT NOT NULL CHECK (length(commit_message) > 0),
    remote_commit_id_before TEXT NOT NULL,
    local_commit_id_before TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('prepared', 'completed')),
    created_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (run_id, idempotency_key),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE RESTRICT,
    FOREIGN KEY (workspace_id) REFERENCES feature_workspaces(id) ON DELETE RESTRICT
) STRICT;

CREATE UNIQUE INDEX idx_workspace_publications_one_prepared
    ON workspace_publications(workspace_id)
    WHERE status = 'prepared';

CREATE INDEX idx_workspace_publications_workspace_created
    ON workspace_publications(workspace_id, created_at);
