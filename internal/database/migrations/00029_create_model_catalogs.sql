-- +goose Up
CREATE TABLE model_catalogs (
    provider TEXT NOT NULL,
    role TEXT NOT NULL,
    models_json TEXT NOT NULL DEFAULT '[]',
    fetched_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider, role)
) STRICT;

-- +goose Down
DROP TABLE model_catalogs;
