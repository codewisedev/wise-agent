-- +goose Up
-- Phase 3b: Kanban board. Tasks get a column, an order within it, priority,
-- labels and (for imported tasks) where they came from.
ALTER TABLE tasks
    ADD COLUMN board_column    text NOT NULL DEFAULT 'backlog'
                               CHECK (board_column IN ('backlog', 'todo', 'in_progress', 'review', 'done')),
    ADD COLUMN position        double precision NOT NULL DEFAULT 0,
    ADD COLUMN priority        text NOT NULL DEFAULT 'none'
                               CHECK (priority IN ('none', 'low', 'medium', 'high', 'urgent')),
    ADD COLUMN labels          text[] NOT NULL DEFAULT '{}',
    ADD COLUMN source          text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'jira', 'clickup', 'trello')),
    ADD COLUMN integration_id  uuid,
    ADD COLUMN external_id     text,
    ADD COLUMN external_url    text,
    ADD COLUMN external_status text,
    ADD COLUMN synced_at       timestamptz;
CREATE UNIQUE INDEX tasks_external_idx ON tasks (integration_id, external_id) WHERE integration_id IS NOT NULL;
CREATE INDEX tasks_board_idx ON tasks (project_id, board_column, position);

-- Place existing tasks according to their workflow state.
UPDATE tasks SET board_column = CASE status
    WHEN 'in_progress' THEN 'in_progress'
    WHEN 'awaiting_approval' THEN 'review'
    WHEN 'completed' THEN 'done'
    WHEN 'cancelled' THEN 'done'
    ELSE 'todo' END,
    position = extract(epoch FROM created_at);

-- +goose Down
DROP INDEX tasks_board_idx;
DROP INDEX tasks_external_idx;
ALTER TABLE tasks
    DROP COLUMN synced_at, DROP COLUMN external_status, DROP COLUMN external_url, DROP COLUMN external_id,
    DROP COLUMN integration_id, DROP COLUMN source, DROP COLUMN labels, DROP COLUMN priority,
    DROP COLUMN position, DROP COLUMN board_column;
