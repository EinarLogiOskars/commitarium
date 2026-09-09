-- +goose Up

ALTER TABLE feature_workspaces
    ADD COLUMN checkout_relative_path TEXT NOT NULL DEFAULT '';

ALTER TABLE feature_workspaces
    ADD COLUMN checkout_created_at TEXT CHECK (
        (checkout_created_at IS NULL AND checkout_relative_path = '')
        OR (
            checkout_created_at IS NOT NULL
            AND length(trim(checkout_relative_path)) > 0
            AND checkout_relative_path = trim(checkout_relative_path)
        )
    );
