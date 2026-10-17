-- +goose Up

-- Plan review and code review are separate steps (ADR-031): a work order may
-- have its plan reviewed and skip code review ("Reviewed plan"). Skipping the
-- reviewer still skips both.
ALTER TABLE features ADD COLUMN skip_code_review INTEGER NOT NULL DEFAULT 0 CHECK (skip_code_review IN (0, 1));
ALTER TABLE runs ADD COLUMN skip_code_review INTEGER NOT NULL DEFAULT 0 CHECK (skip_code_review IN (0, 1));

-- +goose Down
ALTER TABLE runs DROP COLUMN skip_code_review;
ALTER TABLE features DROP COLUMN skip_code_review;
