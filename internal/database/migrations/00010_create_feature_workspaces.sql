-- +goose Up

CREATE TABLE feature_workspaces (
    feature_id TEXT NOT NULL PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL,
    repository_owner TEXT NOT NULL CHECK (length(trim(repository_owner)) > 0),
    repository_name TEXT NOT NULL CHECK (length(trim(repository_name)) > 0),
    base_branch TEXT NOT NULL CHECK (length(trim(base_branch)) > 0),
    branch_name TEXT NOT NULL CHECK (length(trim(branch_name)) > 0),
    base_commit_id TEXT NOT NULL CHECK (
        (length(base_commit_id) = 40 OR length(base_commit_id) = 64)
        AND base_commit_id = lower(base_commit_id)
    ),
    status TEXT NOT NULL CHECK (status IN ('preparing', 'branch_ready')),
    branch_created_at TEXT,
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    CHECK (
        (status = 'preparing' AND branch_created_at IS NULL)
        OR (status = 'branch_ready' AND branch_created_at IS NOT NULL)
    ),
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE RESTRICT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
) STRICT;

CREATE UNIQUE INDEX idx_feature_workspaces_repository_branch
    ON feature_workspaces(repository_owner, repository_name, branch_name);
