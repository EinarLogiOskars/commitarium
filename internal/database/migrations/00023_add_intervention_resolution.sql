-- +goose Up
ALTER TABLE run_interventions ADD COLUMN resolved_at TEXT;
ALTER TABLE run_interventions ADD COLUMN resume_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE run_interventions ADD COLUMN resolution_action_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE run_interventions DROP COLUMN resolution_action_id;
ALTER TABLE run_interventions DROP COLUMN resume_reason;
ALTER TABLE run_interventions DROP COLUMN resolved_at;
