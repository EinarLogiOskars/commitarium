-- +goose Up

-- How the assistant works in a project (ADR-027): "builds" applies its
-- actions without cards and drives the work (what apps did), "assists" only
-- proposes cards the user approves (what projects did). Existing projects
-- keep what their app flag gave them.
ALTER TABLE projects ADD COLUMN assistant_mode TEXT NOT NULL DEFAULT 'assists'
    CHECK (assistant_mode IN ('builds', 'assists'));

UPDATE projects SET assistant_mode = 'builds' WHERE is_app = 1;

-- +goose Down

ALTER TABLE projects DROP COLUMN assistant_mode;
