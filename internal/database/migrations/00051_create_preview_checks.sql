-- +goose Up

-- A preview check is Commitarium's own run of an app's compose file for the
-- exact commit a preview-setup work order published (ADR-028). The desktop
-- runs it on the host in a temporary compose project and records what each
-- container did.
CREATE TABLE preview_checks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt >= 1),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'passed', 'failed', 'expired')),
    evidence_json TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    claimed_at TEXT,
    completed_at TEXT,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX preview_checks_run_idx ON preview_checks(run_id, created_at, id);
CREATE INDEX preview_checks_status_idx ON preview_checks(status, created_at, id);

-- A work order that sets up the preview merges only after a preview check of
-- its exact commit passed.
ALTER TABLE features ADD COLUMN preview_check INTEGER NOT NULL DEFAULT 0 CHECK (preview_check IN (0, 1));

-- +goose Down
ALTER TABLE features DROP COLUMN preview_check;
DROP TABLE preview_checks;
