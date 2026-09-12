-- +goose Up

ALTER TABLE runs
    ADD COLUMN wait_kind TEXT NOT NULL DEFAULT ''
    CHECK (wait_kind IN ('', 'phase_checkpoint', 'round_cap', 'blocker', 'merge_gate', 'clarification', 'paused'));

ALTER TABLE runs
    ADD COLUMN paused INTEGER NOT NULL DEFAULT 0
    CHECK (paused IN (0, 1));

ALTER TABLE runs
    ADD COLUMN paused_from_wait_kind TEXT NOT NULL DEFAULT ''
    CHECK (paused_from_wait_kind IN ('', 'phase_checkpoint', 'round_cap', 'blocker', 'merge_gate', 'clarification'));

-- Existing waits predate typed reasons. Treat them conservatively as blockers;
-- new transitions always store a precise kind.
UPDATE runs SET wait_kind = 'blocker' WHERE status = 'waiting_for_user';

CREATE TABLE run_actions (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('pause', 'resume')),
    occurred_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX run_actions_run_id_idx ON run_actions(run_id, occurred_at, id);
