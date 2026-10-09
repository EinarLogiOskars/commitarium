-- +goose Up
CREATE TABLE attempt_token_usage (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    input_tokens INTEGER NOT NULL CHECK (input_tokens >= 0),
    cached_input_tokens INTEGER NOT NULL CHECK (cached_input_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL CHECK (cache_write_tokens >= 0),
    output_tokens INTEGER NOT NULL CHECK (output_tokens >= 0),
    recorded_at TEXT NOT NULL CHECK (length(recorded_at) > 0),
    PRIMARY KEY (session_id, attempt_id),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE attempt_token_usage;
