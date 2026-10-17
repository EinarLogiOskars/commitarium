-- +goose Up

-- What a run's latest blocker is (ADR-032): its kind ('agent', 'check',
-- 'environment', 'limit', or 'other'), the check that failed, its detail,
-- and a fingerprint that recognises the same failure when it repeats.
CREATE TABLE run_blockers (
    run_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'check', 'environment', 'limit', 'other')),
    check_name TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) > 0),
    recorded_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

-- Each recovery tried on a blocked run, by the user or the assistant, with the
-- blocker fingerprint it answered, so a move that already failed on the same
-- failure is not tried again.
CREATE TABLE run_recoveries (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    how TEXT NOT NULL CHECK (how IN ('recheck', 'continue', 'message', 'fresh_conversation', 'back_to_ready')),
    guidance TEXT NOT NULL DEFAULT '',
    from_assistant INTEGER NOT NULL DEFAULT 0 CHECK (from_assistant IN (0, 1)),
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX run_recoveries_run_idx ON run_recoveries(run_id, created_at);

-- +goose Down
DROP INDEX run_recoveries_run_idx;
DROP TABLE run_recoveries;
DROP TABLE run_blockers;
