-- +goose Up

-- A durable claim fences new run admission while external work-order artifacts
-- are removed. The feature row remains until cleanup finishes, so a coordinator
-- restart can safely retry the same branch, pull request, and checkout cleanup.
CREATE TABLE feature_deletions (
    feature_id TEXT NOT NULL PRIMARY KEY,
    requested_at TEXT NOT NULL CHECK (length(requested_at) > 0),
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE
) STRICT;

-- +goose StatementBegin
CREATE TRIGGER prevent_run_for_deleting_feature
BEFORE INSERT ON runs
WHEN EXISTS (
    SELECT 1 FROM feature_deletions WHERE feature_id = NEW.feature_id
)
BEGIN
    SELECT RAISE(ABORT, 'feature deletion in progress');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER prevent_run_for_deleting_feature;
DROP TABLE feature_deletions;
