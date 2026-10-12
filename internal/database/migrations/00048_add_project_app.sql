-- +goose Up

-- A project the user uses as an app (APPS_PLAN.md): it shows in the Apps
-- area and runs in its own window.
ALTER TABLE projects ADD COLUMN is_app INTEGER NOT NULL DEFAULT 0
    CHECK (is_app IN (0, 1));

-- +goose Down

ALTER TABLE projects DROP COLUMN is_app;
