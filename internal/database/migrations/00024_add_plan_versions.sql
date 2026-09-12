-- +goose Up

ALTER TABLE runs
    ADD COLUMN plan_version INTEGER NOT NULL DEFAULT 1
    CHECK (plan_version > 0);

ALTER TABLE planning_messages
    ADD COLUMN plan_version INTEGER NOT NULL DEFAULT 1
    CHECK (plan_version > 0);

CREATE TABLE run_plan_revisions (
    run_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 1),
    intervention_id TEXT NOT NULL UNIQUE,
    previous_plan_event_id TEXT NOT NULL,
    effective_goal TEXT NOT NULL CHECK (length(trim(effective_goal)) > 0),
    baseline_commit_id TEXT NOT NULL CHECK (length(baseline_commit_id) IN (40, 64)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, version),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (intervention_id) REFERENCES run_interventions(id) ON DELETE RESTRICT,
    FOREIGN KEY (previous_plan_event_id) REFERENCES session_events(id) ON DELETE RESTRICT
);

-- +goose Down

DROP TABLE run_plan_revisions;
ALTER TABLE planning_messages DROP COLUMN plan_version;
ALTER TABLE runs DROP COLUMN plan_version;
