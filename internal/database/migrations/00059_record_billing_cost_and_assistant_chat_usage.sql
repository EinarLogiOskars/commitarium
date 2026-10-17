-- +goose Up

-- How each attempt's account pays ('api_key', 'subscription', or '' when the
-- provider did not say) and the provider's own cost for it, when reported
-- (Claude Code reports one; Codex does not). The cost cards price API-key
-- turns from these, or from the model guide's prices.
ALTER TABLE attempt_token_usage ADD COLUMN billing TEXT NOT NULL DEFAULT '';
ALTER TABLE attempt_token_usage ADD COLUMN cost_usd REAL;
ALTER TABLE assistant_turn_usage ADD COLUMN billing TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_turn_usage ADD COLUMN cost_usd REAL;

-- The assistant's own conversation turns, per scope (a project's ID, or
-- 'commitarium' for the top level), so a project's total includes them.
CREATE TABLE assistant_chat_usage (
    scope TEXT NOT NULL CHECK (length(trim(scope)) > 0),
    attempt_id TEXT NOT NULL CHECK (length(trim(attempt_id)) > 0),
    agent_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    billing TEXT NOT NULL DEFAULT '',
    cost_usd REAL,
    input_tokens INTEGER NOT NULL CHECK (input_tokens >= 0),
    cached_input_tokens INTEGER NOT NULL CHECK (cached_input_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL CHECK (cache_write_tokens >= 0),
    output_tokens INTEGER NOT NULL CHECK (output_tokens >= 0),
    recorded_at TEXT NOT NULL CHECK (length(recorded_at) > 0),
    PRIMARY KEY (scope, attempt_id)
) STRICT;

-- +goose Down
DROP TABLE assistant_chat_usage;
ALTER TABLE assistant_turn_usage DROP COLUMN cost_usd;
ALTER TABLE assistant_turn_usage DROP COLUMN billing;
ALTER TABLE attempt_token_usage DROP COLUMN cost_usd;
ALTER TABLE attempt_token_usage DROP COLUMN billing;
