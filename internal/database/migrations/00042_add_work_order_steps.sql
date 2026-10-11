-- +goose Up

-- Optional work-order steps (ADR-018). Zero is the full flow, so existing
-- work orders and runs keep it.
ALTER TABLE features ADD COLUMN skip_plan INTEGER NOT NULL DEFAULT 0 CHECK (skip_plan IN (0, 1));
ALTER TABLE features ADD COLUMN skip_reviewer INTEGER NOT NULL DEFAULT 0 CHECK (skip_reviewer IN (0, 1));
ALTER TABLE runs ADD COLUMN skip_plan INTEGER NOT NULL DEFAULT 0 CHECK (skip_plan IN (0, 1));
ALTER TABLE runs ADD COLUMN skip_reviewer INTEGER NOT NULL DEFAULT 0 CHECK (skip_reviewer IN (0, 1));

-- +goose Down
ALTER TABLE runs DROP COLUMN skip_reviewer;
ALTER TABLE runs DROP COLUMN skip_plan;
ALTER TABLE features DROP COLUMN skip_reviewer;
ALTER TABLE features DROP COLUMN skip_plan;
