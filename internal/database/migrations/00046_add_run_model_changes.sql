-- +goose Up

-- A run's models can change while it works (the user picked the wrong one):
-- each change is kept, and a turn stopped to switch models is marked so it
-- resumes on the new model instead of ending the run.
CREATE TABLE run_model_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('lead', 'reviewer')),
    from_model TEXT NOT NULL,
    to_model TEXT NOT NULL,
    stopped INTEGER NOT NULL CHECK (stopped IN (0, 1)),
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_run_model_changes_run ON run_model_changes(run_id, id);

CREATE TABLE run_model_switches (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, attempt_id)
) STRICT;

-- The model an attempt ran on, so usage shows what each model cost.
ALTER TABLE attempt_token_usage ADD COLUMN model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE attempt_token_usage DROP COLUMN model;
DROP TABLE run_model_switches;
DROP TABLE run_model_changes;
