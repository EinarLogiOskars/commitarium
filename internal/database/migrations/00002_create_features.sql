-- +goose Up

CREATE TABLE features (
    id TEXT NOT NULL PRIMARY KEY,
    project_id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    description TEXT NOT NULL,
    state TEXT NOT NULL CHECK (length(state) > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    updated_at TEXT NOT NULL CHECK (length(updated_at) > 0),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX idx_features_project_id ON features(project_id);
