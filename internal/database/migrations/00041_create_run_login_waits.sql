-- +goose Up

-- A run paused because an agent's provider login expired names that agent
-- here until the run continues (ADR-017).
CREATE TABLE run_login_waits (
    run_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL CHECK (length(agent_id) > 0),
    role TEXT NOT NULL CHECK (role IN ('lead', 'reviewer')),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX idx_run_login_waits_agent ON run_login_waits(agent_id);

-- +goose Down
DROP TABLE run_login_waits;
