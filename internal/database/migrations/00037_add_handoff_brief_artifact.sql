-- +goose Up

-- SQLite cannot widen a CHECK constraint in place. Rebuild the artifact table
-- so a work order's handoff brief can be stored beside the existing kinds.
CREATE TABLE feature_artifacts_new (
    feature_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('goal_draft', 'implementation_plan', 'acceptance_tests', 'handoff_brief')),
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
