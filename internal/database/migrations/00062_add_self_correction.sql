-- +goose Up

-- A refused turn Commitarium hands back to its agent is a recovery of its own
-- ('self_correction'), and a check that keeps refusing a turn the agent put
-- right is likely Commitarium's own problem ('commitarium'; ADR-032).
CREATE TABLE run_blockers_next (
    run_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'check', 'environment', 'limit', 'other', 'commitarium')),
    check_name TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) > 0),
    recorded_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT INTO run_blockers_next SELECT run_id, kind, check_name, detail, reason, fingerprint, recorded_at FROM run_blockers;
DROP TABLE run_blockers;
ALTER TABLE run_blockers_next RENAME TO run_blockers;

CREATE TABLE run_recoveries_next (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    how TEXT NOT NULL CHECK (how IN ('recheck', 'continue', 'message', 'fresh_conversation', 'back_to_ready', 'self_correction')),
    guidance TEXT NOT NULL DEFAULT '',
    from_assistant INTEGER NOT NULL DEFAULT 0 CHECK (from_assistant IN (0, 1)),
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT INTO run_recoveries_next SELECT id, run_id, fingerprint, how, guidance, from_assistant, created_at FROM run_recoveries;
DROP INDEX run_recoveries_run_idx;
DROP TABLE run_recoveries;
ALTER TABLE run_recoveries_next RENAME TO run_recoveries;
CREATE INDEX run_recoveries_run_idx ON run_recoveries(run_id, created_at);

-- +goose Down

CREATE TABLE run_blockers_prev (
    run_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('agent', 'check', 'environment', 'limit', 'other')),
    check_name TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL CHECK (length(fingerprint) > 0),
    recorded_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT INTO run_blockers_prev
    SELECT run_id, CASE kind WHEN 'commitarium' THEN 'check' ELSE kind END, check_name, detail, reason, fingerprint, recorded_at
    FROM run_blockers;
DROP TABLE run_blockers;
ALTER TABLE run_blockers_prev RENAME TO run_blockers;

CREATE TABLE run_recoveries_prev (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    how TEXT NOT NULL CHECK (how IN ('recheck', 'continue', 'message', 'fresh_conversation', 'back_to_ready')),
    guidance TEXT NOT NULL DEFAULT '',
    from_assistant INTEGER NOT NULL DEFAULT 0 CHECK (from_assistant IN (0, 1)),
    created_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
INSERT INTO run_recoveries_prev
    SELECT id, run_id, fingerprint, CASE how WHEN 'self_correction' THEN 'continue' ELSE how END, guidance, from_assistant, created_at
    FROM run_recoveries;
DROP INDEX run_recoveries_run_idx;
DROP TABLE run_recoveries;
ALTER TABLE run_recoveries_prev RENAME TO run_recoveries;
CREATE INDEX run_recoveries_run_idx ON run_recoveries(run_id, created_at);
