-- +goose Up

-- Independent acceptance tests were retired (ADR-031) and their code is gone.
-- Drop what stored them: the setting on projects, work orders and runs, the
-- reviewer's checklist artifacts and the events that recorded them, and the
-- coordinator's own runs of the tests with the columns only they used.

ALTER TABLE projects DROP COLUMN independent_acceptance_tests;
ALTER TABLE features DROP COLUMN independent_acceptance_tests;
ALTER TABLE runs DROP COLUMN independent_acceptance_tests;

-- An artifact event names its kind; one of a kind that no longer exists
-- could not be read back.
DELETE FROM workflow_events
WHERE event_type = 'feature.artifact_updated'
  AND json_extract(payload, '$.kind') = 'acceptance_tests';

DELETE FROM feature_artifacts WHERE kind = 'acceptance_tests';

-- SQLite cannot narrow a CHECK constraint in place. Rebuild the artifact table
-- without the retired kind.
CREATE TABLE feature_artifacts_new (
    feature_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('goal_draft', 'implementation_plan', 'handoff_brief')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    document TEXT NOT NULL CHECK (json_valid(document)),
    actor_kind TEXT NOT NULL CHECK (length(actor_kind) > 0),
    actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    PRIMARY KEY (feature_id, kind, revision),
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE
) STRICT;

INSERT INTO feature_artifacts_new (
    feature_id, kind, revision, document, actor_kind, actor_id, updated_at
)
SELECT feature_id, kind, revision, document, actor_kind, actor_id, updated_at
FROM feature_artifacts;

DROP TABLE feature_artifacts;
ALTER TABLE feature_artifacts_new RENAME TO feature_artifacts;

CREATE INDEX idx_feature_artifacts_latest
    ON feature_artifacts(feature_id, kind, revision DESC);

DELETE FROM validation_jobs WHERE kind = 'acceptance';
ALTER TABLE validation_jobs DROP COLUMN merge_commit_id;
ALTER TABLE validation_jobs DROP COLUMN labels_json;
ALTER TABLE validation_jobs DROP COLUMN kind;
