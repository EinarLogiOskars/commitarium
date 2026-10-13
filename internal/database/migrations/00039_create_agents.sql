-- +goose Up

-- Agents are provider accounts (ADR-016). The two existing subscriptions
-- become the "codex" and "claude" agents, whose IDs match the provider values
-- already stored on projects, features, and runs. A new install has no
-- subscriptions to carry over and starts with no agents (ADR-027): the user
-- adds each one. Installs that ran this migration before keep theirs.
CREATE TABLE agents (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(trim(id)) > 0),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    provider TEXT NOT NULL CHECK (provider IN ('codex', 'claude')),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0)
) STRICT;

INSERT INTO agents (id, name, provider, created_at, updated_at)
SELECT seeded.id, seeded.name, seeded.provider,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  FROM (SELECT 'codex' AS id, 'Codex' AS name, 'codex' AS provider
        UNION ALL SELECT 'claude', 'Claude', 'claude') AS seeded
 WHERE EXISTS (SELECT 1 FROM projects);

-- +goose Down
DROP TABLE agents;
