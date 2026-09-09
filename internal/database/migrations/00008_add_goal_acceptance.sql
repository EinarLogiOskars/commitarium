-- +goose Up

ALTER TABLE features ADD COLUMN accepted_goal TEXT NOT NULL DEFAULT '';

ALTER TABLE features ADD COLUMN goal_accepted_at TEXT
    CHECK (
        (goal_accepted_at IS NULL AND accepted_goal = '')
        OR
        (
            goal_accepted_at IS NOT NULL
            AND length(trim(accepted_goal)) > 0
        )
    );
