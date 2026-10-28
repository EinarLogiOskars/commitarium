-- +goose Up

-- Work orders whose gates passed and that merge on their own wait here, one
-- line per project, and merge one at a time in the order they joined
-- (ADR-036). A run leaves the line when it merges, blocks, pauses, or ends.
CREATE TABLE merge_queue (
    run_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL CHECK (length(project_id) > 0),
    joined_at TEXT NOT NULL CHECK (length(joined_at) > 0),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) STRICT;
CREATE INDEX idx_merge_queue_project ON merge_queue(project_id, joined_at);

-- +goose Down

DROP TABLE merge_queue;
