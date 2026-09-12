-- +goose Up

ALTER TABLE session_events ADD COLUMN activity_json TEXT
    CHECK (activity_json IS NULL OR json_valid(activity_json));

-- +goose Down

ALTER TABLE session_events DROP COLUMN activity_json;
