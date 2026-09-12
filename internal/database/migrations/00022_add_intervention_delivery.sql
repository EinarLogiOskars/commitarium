-- +goose Up

ALTER TABLE run_interventions
    ADD COLUMN attempt_id TEXT NOT NULL DEFAULT '';

ALTER TABLE run_interventions
    ADD COLUMN effect TEXT NOT NULL DEFAULT '' CHECK (
        effect IN ('', 'guidance_applied', 'clarification_required', 'replanning_required')
    );
