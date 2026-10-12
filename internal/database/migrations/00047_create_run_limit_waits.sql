-- +goose Up

-- A run paused because an agent reached its provider usage limit names that
-- agent here, with when the limit resets, until the run continues. The
-- coordinator continues it at the reset time, or retries every so often when
-- the provider gave none.
CREATE TABLE run_limit_waits (
    run_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL CHECK (length(agent_id) > 0),
    role TEXT NOT NULL CHECK (role IN ('lead', 'reviewer')),
    resets_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX idx_run_limit_waits_agent ON run_limit_waits(agent_id);

-- +goose Down
DROP TABLE run_limit_waits;
