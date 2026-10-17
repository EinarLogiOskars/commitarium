-- +goose Up

-- The user's tier for a model, overriding the official model guide that
-- ships with Commitarium (ADR-031). official_tier is the official tier the
-- override replaced ('' when the model was unrated), so a release that
-- changes it can be shown to the user.
CREATE TABLE model_guide_overrides (
    model_id TEXT PRIMARY KEY CHECK (length(model_id) BETWEEN 1 AND 128),
    tier TEXT NOT NULL CHECK (tier IN ('premium', 'frontier', 'strong', 'light', 'avoid')),
    official_tier TEXT NOT NULL CHECK (official_tier IN ('', 'premium', 'frontier', 'strong', 'light', 'avoid')),
    updated_at TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE model_guide_overrides;
