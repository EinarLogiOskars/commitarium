-- +goose Up

-- A run whose turn infrastructure cut off (a worker restart, a lost
-- connection) retries it by itself a few times. The row keeps how many
-- retries have started and when the next is due, so a coordinator restart
-- continues the schedule. It goes once the turn completes or blocks.
CREATE TABLE run_retry_waits (
    run_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL CHECK (length(session_id) > 0),
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) > 0),
    retries INTEGER NOT NULL CHECK (retries >= 0),
    cause TEXT NOT NULL,
    due_at TEXT NOT NULL DEFAULT '',
    turn TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE run_retry_waits;
