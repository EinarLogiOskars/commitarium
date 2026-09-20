-- +goose Up

CREATE TABLE feature_artifacts (
    feature_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('goal_draft', 'implementation_plan')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    document TEXT NOT NULL CHECK (json_valid(document)),
    actor_kind TEXT NOT NULL CHECK (length(actor_kind) > 0),
    actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    PRIMARY KEY (feature_id, kind, revision),
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX idx_feature_artifacts_latest
    ON feature_artifacts(feature_id, kind, revision DESC);

-- +goose Down

DROP INDEX idx_feature_artifacts_latest;
DROP TABLE feature_artifacts;
