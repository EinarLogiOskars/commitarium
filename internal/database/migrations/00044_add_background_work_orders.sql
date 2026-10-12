-- +goose Up

-- A background work order is one Commitarium starts by itself, such as
-- setting up a project's preview. It runs like any work order but is not
-- listed with the user's work.
ALTER TABLE features ADD COLUMN background INTEGER NOT NULL DEFAULT 0 CHECK (background IN (0, 1));

-- +goose Down
ALTER TABLE features DROP COLUMN background;
