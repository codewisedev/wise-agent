-- +goose Up
-- Imported tasks a source stops returning (e.g. no longer assigned to you) are
-- archived, not deleted, and restored if the source returns them again.
ALTER TABLE tasks ADD COLUMN archived_at timestamptz;
CREATE INDEX tasks_live_idx ON tasks (project_id) WHERE archived_at IS NULL;

-- +goose Down
DROP INDEX tasks_live_idx;
ALTER TABLE tasks DROP COLUMN archived_at;
