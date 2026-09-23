-- +goose Up

ALTER TABLE worker_attempts
ADD COLUMN launch_request_json TEXT
    CHECK (launch_request_json IS NULL OR json_valid(launch_request_json));

-- Attempts created before this migration remain inspectable but cannot be
-- relaunched automatically because their original output contract and
-- workspace-access request were not stored.

-- +goose Down

ALTER TABLE worker_attempts DROP COLUMN launch_request_json;
