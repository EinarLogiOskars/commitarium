-- +goose Up

ALTER TABLE projects
    ADD COLUMN autonomy_policy TEXT NOT NULL DEFAULT 'review_each_phase'
    CHECK (autonomy_policy IN ('review_each_phase', 'run_to_completion'));

ALTER TABLE runs
    ADD COLUMN autonomy_policy TEXT NOT NULL DEFAULT 'review_each_phase'
    CHECK (autonomy_policy IN ('review_each_phase', 'run_to_completion'));
