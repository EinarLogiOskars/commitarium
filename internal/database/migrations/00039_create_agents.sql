-- +goose Up

-- Agents are provider accounts (ADR-016). The two existing subscriptions
-- become the "codex" and "claude" agents, whose IDs match the provider values
-- already stored on projects, features, and runs.
CREATE TABLE agents (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(trim(id)) > 0),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    provider TEXT NOT NULL CHECK (provider IN ('codex', 'claude')),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
) STRICT;

INSERT INTO agents (id, name, provider, created_at, updated_at) VALUES
    ('codex', 'Codex', 'codex', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('claude', 'Claude', 'claude', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

-- +goose Down
DROP TABLE agents;
