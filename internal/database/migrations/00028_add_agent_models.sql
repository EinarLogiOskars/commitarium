-- +goose Up

-- Empty values preserve pre-model-selection projects and admitted work. New
-- work orders require explicit non-empty IDs at the service boundary.
ALTER TABLE projects ADD COLUMN lead_model TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN reviewer_model TEXT NOT NULL DEFAULT '';

ALTER TABLE features ADD COLUMN lead_model TEXT NOT NULL DEFAULT '';
ALTER TABLE features ADD COLUMN reviewer_model TEXT NOT NULL DEFAULT '';

ALTER TABLE runs ADD COLUMN lead_model TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN reviewer_model TEXT NOT NULL DEFAULT '';

UPDATE features AS feature
SET lead_model = COALESCE(
        NULLIF((SELECT lead_model FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1), ''),
        (SELECT lead_model FROM projects WHERE id = feature.project_id),
        ''
    ),
    reviewer_model = COALESCE(
        NULLIF((SELECT reviewer_model FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1), ''),
        (SELECT reviewer_model FROM projects WHERE id = feature.project_id),
        ''
    );
