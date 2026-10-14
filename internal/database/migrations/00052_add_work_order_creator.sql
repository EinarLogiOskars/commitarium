-- +goose Up

-- Who created a work order (ADR-027, ownership): the user by hand, or the
-- assistant (its actions, and Commitarium's own setup work that runs on the
-- assistant's agent). The assistant acts on its own only on its work; the
-- user's it only mentions. Existing rows are the user's, except Commitarium's
-- own setup work; the coordinator marks the ones the assistant created from
-- its conversations' applied actions when it starts.
ALTER TABLE features ADD COLUMN created_by TEXT NOT NULL DEFAULT 'user'
    CHECK (created_by IN ('user', 'assistant'));

UPDATE features SET created_by = 'assistant'
 WHERE background = 1 OR title = 'Set the project up';

-- +goose Down

ALTER TABLE features DROP COLUMN created_by;
