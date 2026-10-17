-- +goose Up

-- A work order's optional choices by phase (ADR-031): the model and effort
-- each role plans with, and its effort after the plan. Chosen at Start with
-- the work order's other settings; turns read them when they are routed.
CREATE TABLE work_order_phase_choices (
    feature_id TEXT PRIMARY KEY,
    lead_plan_model TEXT NOT NULL DEFAULT '',
    lead_plan_effort TEXT NOT NULL DEFAULT '',
    lead_effort TEXT NOT NULL DEFAULT '',
    reviewer_plan_model TEXT NOT NULL DEFAULT '',
    reviewer_plan_effort TEXT NOT NULL DEFAULT '',
    reviewer_effort TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE work_order_phase_choices;
