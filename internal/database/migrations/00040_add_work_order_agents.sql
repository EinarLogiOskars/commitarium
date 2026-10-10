-- +goose Up

-- Work orders and project defaults name the lead and reviewer agents
-- (ADR-016). The provider columns stay, always the agents' providers. Existing
-- rows use the agents migrated from the provider profiles, whose IDs are the
-- provider names.
ALTER TABLE projects ADD COLUMN lead_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN reviewer_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE features ADD COLUMN lead_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE features ADD COLUMN reviewer_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lead_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN reviewer_agent TEXT NOT NULL DEFAULT '';

UPDATE projects SET lead_agent = lead_provider, reviewer_agent = reviewer_provider;
UPDATE features SET lead_agent = lead_provider, reviewer_agent = reviewer_provider;
UPDATE runs SET lead_agent = lead_provider, reviewer_agent = reviewer_provider;

-- +goose Down
ALTER TABLE runs DROP COLUMN reviewer_agent;
ALTER TABLE runs DROP COLUMN lead_agent;
ALTER TABLE features DROP COLUMN reviewer_agent;
ALTER TABLE features DROP COLUMN lead_agent;
ALTER TABLE projects DROP COLUMN reviewer_agent;
ALTER TABLE projects DROP COLUMN lead_agent;
