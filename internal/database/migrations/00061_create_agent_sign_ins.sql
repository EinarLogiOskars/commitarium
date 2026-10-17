-- +goose Up

-- An agent whose provider login failed past the automatic retries needs the
-- user to sign in again (LOGIN_RECOVERY_PLAN.md). The record goes when a
-- sign-in finishes in the app or a turn on the agent succeeds; meanwhile
-- the coordinator tries the waiting work again now and then (last_tried_at).
CREATE TABLE agent_sign_ins (
    agent_id TEXT PRIMARY KEY CHECK (length(agent_id) > 0),
    since TEXT NOT NULL CHECK (length(since) > 0),
    last_error TEXT NOT NULL DEFAULT '',
    last_tried_at TEXT NOT NULL CHECK (length(last_tried_at) > 0)
) STRICT;

-- A run may wait on both its agents' logins: one row per run and role.
CREATE TABLE run_login_waits_by_role (
    run_id TEXT NOT NULL,
    agent_id TEXT NOT NULL CHECK (length(agent_id) > 0),
    role TEXT NOT NULL CHECK (role IN ('lead', 'reviewer')),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    PRIMARY KEY (run_id, role),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT INTO run_login_waits_by_role (run_id, agent_id, role, created_at)
    SELECT run_id, agent_id, role, created_at FROM run_login_waits;
DROP TABLE run_login_waits;
ALTER TABLE run_login_waits_by_role RENAME TO run_login_waits;
CREATE INDEX idx_run_login_waits_agent ON run_login_waits(agent_id);

-- +goose Down

CREATE TABLE run_login_waits_by_run (
    run_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL CHECK (length(agent_id) > 0),
    role TEXT NOT NULL CHECK (role IN ('lead', 'reviewer')),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT OR IGNORE INTO run_login_waits_by_run (run_id, agent_id, role, created_at)
    SELECT run_id, agent_id, role, created_at FROM run_login_waits;
DROP TABLE run_login_waits;
ALTER TABLE run_login_waits_by_run RENAME TO run_login_waits;
CREATE INDEX idx_run_login_waits_agent ON run_login_waits(agent_id);
DROP TABLE agent_sign_ins;
