-- +goose Up

CREATE TABLE project_environment_requests (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    packages_json TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    status TEXT NOT NULL CHECK (
        status IN ('requested', 'approved', 'provisioning', 'ready', 'rejected', 'failed')
    ),
    resolved_packages_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    UNIQUE (session_id, attempt_id)
) STRICT;

CREATE INDEX project_environment_requests_project_status_idx
    ON project_environment_requests(project_id, status, requested_at, id);

CREATE TABLE project_validation_configs (
    project_id TEXT PRIMARY KEY,
    commands_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE validation_jobs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    commands_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'passed', 'failed')),
    results_json TEXT NOT NULL DEFAULT '[]',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX validation_jobs_run_status_idx
    ON validation_jobs(run_id, status, created_at, id);
