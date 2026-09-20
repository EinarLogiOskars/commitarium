-- +goose Up
ALTER TABLE session_events ADD COLUMN stream_id TEXT;

-- +goose Down
ALTER TABLE session_events DROP COLUMN stream_id;
