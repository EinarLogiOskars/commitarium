-- +goose Up

ALTER TABLE projects
    ADD COLUMN planning_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (planning_round_limit >= 0);

ALTER TABLE projects
    ADD COLUMN implementation_review_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (implementation_review_round_limit >= 0);

ALTER TABLE runs
    ADD COLUMN planning_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (planning_round_limit >= 0);

ALTER TABLE runs
    ADD COLUMN implementation_review_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (implementation_review_round_limit >= 0);
