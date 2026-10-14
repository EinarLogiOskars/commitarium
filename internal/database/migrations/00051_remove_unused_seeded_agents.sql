-- +goose Up

-- A new install starts with no agents (ADR-027): the user adds each one.
-- 00039 seeded the "codex" and "claude" agents for every install; remove them
-- where nothing was ever built with them. Installs with projects keep them,
-- since projects, work orders, and runs name these agents.
DELETE FROM agents
 WHERE id IN ('codex', 'claude')
   AND NOT EXISTS (SELECT 1 FROM projects);

-- +goose Down
-- The seeded agents are not restored: an install that went back would have
-- removed them by hand the same way.
SELECT 1;
