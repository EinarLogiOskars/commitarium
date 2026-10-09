-- +goose Up
CREATE TABLE planning_round_extensions (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    previous_limit INTEGER NOT NULL CHECK (previous_limit > 0),
    extended_limit INTEGER NOT NULL CHECK (extended_limit = previous_limit + 1),
    occurred_at TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX planning_round_extensions_run_id_idx
    ON planning_round_extensions(run_id, occurred_at, id);

-- +goose Down
DROP INDEX planning_round_extensions_run_id_idx;
DROP TABLE planning_round_extensions;
