-- +goose Up

-- The reasoning effort an attempt ran at ('' for the model's default), so
-- the cost breakdown can show it beside the model (ADR-031).
ALTER TABLE attempt_token_usage ADD COLUMN effort TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE attempt_token_usage DROP COLUMN effort;
