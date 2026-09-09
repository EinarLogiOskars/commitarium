-- +goose Up

ALTER TABLE projects ADD COLUMN forgejo_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN forgejo_repository TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN forgejo_default_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN forgejo_bound_at TEXT
    CHECK (
        (
            forgejo_bound_at IS NULL
            AND forgejo_owner = ''
            AND forgejo_repository = ''
            AND forgejo_default_branch = ''
        )
        OR
        (
            forgejo_bound_at IS NOT NULL
            AND length(trim(forgejo_owner)) > 0
            AND length(trim(forgejo_repository)) > 0
            AND length(trim(forgejo_default_branch)) > 0
        )
    );
