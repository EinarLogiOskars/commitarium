-- +goose Up
-- A round-limit extension also extends a code review's rounds, not only a
-- planning discussion's.
ALTER TABLE planning_round_extensions ADD COLUMN stage TEXT NOT NULL DEFAULT 'planning'
    CHECK (stage IN ('planning', 'review'));

-- +goose Down
ALTER TABLE planning_round_extensions DROP COLUMN stage;
