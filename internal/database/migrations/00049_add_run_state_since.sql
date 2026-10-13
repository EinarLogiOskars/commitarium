-- +goose Up

-- When a run entered its current status, wait and reason: when it blocked,
-- for a blocked run. updated_at moves on while a run waits (a model change),
-- so it cannot say how long a blocker has waited. Triggers keep it, so every
-- store that moves a run keeps it right.
ALTER TABLE runs ADD COLUMN state_since TEXT NOT NULL DEFAULT '';
UPDATE runs SET state_since = updated_at;

-- +goose StatementBegin
CREATE TRIGGER runs_state_since_on_insert
AFTER INSERT ON runs
WHEN NEW.state_since = ''
BEGIN
    UPDATE runs SET state_since = NEW.updated_at WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER runs_state_since_on_change
AFTER UPDATE OF status, wait_kind, reason ON runs
WHEN NEW.status IS NOT OLD.status OR NEW.wait_kind IS NOT OLD.wait_kind OR NEW.reason IS NOT OLD.reason
BEGIN
    UPDATE runs SET state_since = NEW.updated_at WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER runs_state_since_on_change;
DROP TRIGGER runs_state_since_on_insert;
ALTER TABLE runs DROP COLUMN state_since;
