-- +goose Up

-- A work order is either a work order or an interactive session (ADR-019).
ALTER TABLE features ADD COLUMN kind TEXT NOT NULL DEFAULT 'work_order'
    CHECK (kind IN ('work_order', 'session'));

-- +goose Down
ALTER TABLE features DROP COLUMN kind;
