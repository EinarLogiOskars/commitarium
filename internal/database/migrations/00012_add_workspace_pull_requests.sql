-- +goose Up

ALTER TABLE feature_workspaces
    ADD COLUMN pull_request_number INTEGER CHECK (
        pull_request_number IS NULL OR pull_request_number > 0
    );

ALTER TABLE feature_workspaces
    ADD COLUMN pull_request_url TEXT NOT NULL DEFAULT '';

ALTER TABLE feature_workspaces
    ADD COLUMN pull_request_recorded_at TEXT CHECK (
        (
            pull_request_number IS NULL
            AND pull_request_url = ''
            AND pull_request_recorded_at IS NULL
        )
        OR (
            pull_request_number IS NOT NULL
            AND length(trim(pull_request_url)) > 0
            AND pull_request_url = trim(pull_request_url)
            AND pull_request_recorded_at IS NOT NULL
        )
    );
