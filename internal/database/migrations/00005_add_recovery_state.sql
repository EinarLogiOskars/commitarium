-- +goose Up

ALTER TABLE projects
    ADD COLUMN recovery_policy TEXT NOT NULL DEFAULT 'approval_required'
    CHECK (recovery_policy IN ('approval_required', 'automatic'));

ALTER TABLE sessions ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN disposition TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN recovery_attempt INTEGER NOT NULL DEFAULT 0
    CHECK (recovery_attempt >= 0);
