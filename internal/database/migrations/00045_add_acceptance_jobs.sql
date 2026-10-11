-- +goose Up

-- An acceptance job is the coordinator's own run of a work order's
-- independent acceptance tests against an implementation commit, apart from
-- the reviewer. It merges the reviewer's private test commit, read from the
-- reviewer's checkout, into the commit first and runs every test's command,
-- labelled by test ID.
ALTER TABLE validation_jobs ADD COLUMN kind TEXT NOT NULL DEFAULT 'validation'
    CHECK (kind IN ('validation', 'acceptance'));
ALTER TABLE validation_jobs ADD COLUMN labels_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE validation_jobs ADD COLUMN merge_commit_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE validation_jobs DROP COLUMN merge_commit_id;
ALTER TABLE validation_jobs DROP COLUMN labels_json;
ALTER TABLE validation_jobs DROP COLUMN kind;
