-- +goose Up

-- A work order's clarification conversation with the project assistant
-- (ADR-015). One session per feature; it is removed with the feature.
CREATE TABLE assistant_sessions (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(trim(id)) > 0),
    project_id TEXT NOT NULL CHECK (length(trim(project_id)) > 0),
    feature_id TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL CHECK (length(provider) > 0),
    model TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'waiting_for_user', 'proposal_ready', 'failed')),
    message TEXT NOT NULL,
    workspace_id TEXT NOT NULL CHECK (length(trim(workspace_id)) > 0),
    base_commit_id TEXT NOT NULL,
    provider_session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    turn INTEGER NOT NULL CHECK (turn > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    FOREIGN KEY (feature_id) REFERENCES features(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE assistant_messages (
    session_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    text TEXT NOT NULL CHECK (length(trim(text)) > 0),
    occurred_at TEXT NOT NULL CHECK (length(occurred_at) > 0),
    PRIMARY KEY (session_id, sequence),
    FOREIGN KEY (session_id) REFERENCES assistant_sessions(id) ON DELETE CASCADE
) STRICT;

-- Replies are idempotent: one key always maps to one attempt.
CREATE TABLE assistant_mutations (
    session_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) > 0),
    digest TEXT NOT NULL CHECK (length(digest) > 0),
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    PRIMARY KEY (session_id, idempotency_key),
    FOREIGN KEY (session_id) REFERENCES assistant_sessions(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE assistant_turn_usage (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    input_tokens INTEGER NOT NULL CHECK (input_tokens >= 0),
    cached_input_tokens INTEGER NOT NULL CHECK (cached_input_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL CHECK (cache_write_tokens >= 0),
    output_tokens INTEGER NOT NULL CHECK (output_tokens >= 0),
    recorded_at TEXT NOT NULL CHECK (length(recorded_at) > 0),
    PRIMARY KEY (session_id, attempt_id),
    FOREIGN KEY (session_id) REFERENCES assistant_sessions(id) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE assistant_turn_usage;
DROP TABLE assistant_mutations;
DROP TABLE assistant_messages;
DROP TABLE assistant_sessions;
