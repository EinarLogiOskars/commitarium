-- +goose Up

ALTER TABLE projects
    ADD COLUMN lead_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (lead_provider IN ('codex', 'claude'));

ALTER TABLE projects
    ADD COLUMN reviewer_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (reviewer_provider IN ('codex', 'claude'));

ALTER TABLE runs
    ADD COLUMN lead_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (lead_provider IN ('codex', 'claude'));

ALTER TABLE runs
    ADD COLUMN reviewer_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (reviewer_provider IN ('codex', 'claude'));
