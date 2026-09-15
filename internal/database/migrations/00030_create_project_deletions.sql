-- +goose Up

-- Keep a durable tombstone after the project row is removed. It makes an exact
-- retry after a lost success response a no-op while a new key still observes
-- the public project_not_found contract.
CREATE TABLE project_deletions (
    project_id TEXT NOT NULL PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    force INTEGER NOT NULL CHECK (force IN (0, 1)),
    repository_owner TEXT NOT NULL DEFAULT '',
    repository_name TEXT NOT NULL DEFAULT '',
    repository_default_branch TEXT NOT NULL DEFAULT '',
    repository_bound_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'completed')),
    requested_at TEXT NOT NULL CHECK (length(requested_at) > 0),
    completed_at TEXT
) STRICT;

-- +goose StatementBegin
CREATE TRIGGER prevent_feature_for_deleting_project
BEFORE INSERT ON features
WHEN EXISTS (
    SELECT 1 FROM project_deletions
    WHERE project_id = NEW.project_id AND status = 'pending'
)
BEGIN
    SELECT RAISE(ABORT, 'project deletion in progress');
END;
-- +goose StatementEnd

-- Existing features must not admit a new run after the project-wide claim.
-- +goose StatementBegin
CREATE TRIGGER prevent_run_for_deleting_project
BEFORE INSERT ON runs
WHEN EXISTS (
    SELECT 1
    FROM features f
    JOIN project_deletions d ON d.project_id = f.project_id
    WHERE f.id = NEW.feature_id AND d.status = 'pending'
)
BEGIN
    SELECT RAISE(ABORT, 'project deletion in progress');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER prevent_run_for_deleting_project;
DROP TRIGGER prevent_feature_for_deleting_project;
DROP TABLE project_deletions;
