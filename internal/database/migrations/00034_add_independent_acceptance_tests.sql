-- +goose Up

ALTER TABLE projects ADD COLUMN independent_acceptance_tests INTEGER NOT NULL DEFAULT 0
    CHECK (independent_acceptance_tests IN (0, 1));

ALTER TABLE features ADD COLUMN independent_acceptance_tests INTEGER NOT NULL DEFAULT 0
    CHECK (independent_acceptance_tests IN (0, 1));

ALTER TABLE runs ADD COLUMN independent_acceptance_tests INTEGER NOT NULL DEFAULT 0
    CHECK (independent_acceptance_tests IN (0, 1));

-- SQLite cannot widen a CHECK constraint in place. Rebuild the artifact table
-- so the reviewer checklist can be persisted alongside the two existing kinds.
CREATE TABLE feature_artifacts_new (
    feature_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('goal_draft', 'implementation_plan', 'acceptance_tests')),
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
