-- +goose Up

ALTER TABLE projects
    ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'require_user_approval'
    CHECK (merge_policy IN ('require_user_approval', 'auto_after_gates'));

ALTER TABLE runs
    ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'require_user_approval'
    CHECK (merge_policy IN ('require_user_approval', 'auto_after_gates'));

ALTER TABLE feature_workspaces
    ADD COLUMN approved_commit_id TEXT NOT NULL DEFAULT '';

ALTER TABLE feature_workspaces
    ADD COLUMN merge_ready_at TEXT;

ALTER TABLE feature_workspaces
    ADD COLUMN merge_commit_id TEXT NOT NULL DEFAULT '';

ALTER TABLE feature_workspaces
    ADD COLUMN merged_at TEXT;
