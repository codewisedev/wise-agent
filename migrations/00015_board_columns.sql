-- +goose Up
-- Phase 3c: every project has its own board columns. A column may carry a
-- workflow role so runs can move cards on any layout.
CREATE TABLE board_columns (
    id         uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    position   double precision NOT NULL,
    role       text CHECK (role IN ('backlog', 'todo', 'in_progress', 'review', 'done')),
    color      text CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
    wip_limit  int CHECK (wip_limit BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX board_columns_role_idx ON board_columns (project_id, role) WHERE role IS NOT NULL;
CREATE INDEX board_columns_project_idx ON board_columns (project_id, position);

-- Existing projects get the five columns they had, keyed by role.
INSERT INTO board_columns (id, project_id, name, position, role, color)
SELECT gen_random_uuid(), p.id, d.name, d.pos, d.role, d.color
FROM projects p
CROSS JOIN (VALUES
    ('Backlog', 1, 'backlog', '#64748b'),
    ('To do', 2, 'todo', '#3b82f6'),
    ('In progress', 3, 'in_progress', '#f59e0b'),
    ('Review', 4, 'review', '#a855f7'),
    ('Done', 5, 'done', '#22c55e')
) AS d (name, pos, role, color);

ALTER TABLE tasks ADD COLUMN column_id uuid REFERENCES board_columns (id) ON DELETE RESTRICT;
UPDATE tasks t SET column_id = c.id FROM board_columns c WHERE c.project_id = t.project_id AND c.role = t.board_column;
ALTER TABLE tasks ALTER COLUMN column_id SET NOT NULL;
DROP INDEX tasks_board_idx;
ALTER TABLE tasks DROP COLUMN board_column;
CREATE INDEX tasks_board_idx ON tasks (column_id, position);

-- Integration status maps point at column ids instead of fixed keys.
UPDATE integrations i SET status_map = coalesce((
    SELECT jsonb_object_agg(e.key, c.id::text)
    FROM jsonb_each_text(i.status_map) e
    JOIN board_columns c ON c.project_id = i.project_id AND c.role = e.value
), '{}'::jsonb);

-- The shipped board default gained the todo role and colors; update the stored
-- setting only if it is still the old, unedited default.
UPDATE app_settings SET value = '{"columns":[
    {"name":"Backlog","role":"backlog","color":"#64748b"},{"name":"To do","role":"todo","color":"#3b82f6"},
    {"name":"In progress","role":"in_progress","color":"#f59e0b"},{"name":"Review","role":"review","color":"#a855f7"},
    {"name":"Done","role":"done","color":"#22c55e"}]}'::jsonb, version = version + 1, updated_at = now()
WHERE key = 'board_defaults' AND value = '{"columns":[
    {"name":"Backlog","role":"backlog"},{"name":"To do"},{"name":"In progress","role":"in_progress"},
    {"name":"Review","role":"review"},{"name":"Done","role":"done"}]}'::jsonb;

-- +goose Down
UPDATE integrations i SET status_map = coalesce((
    SELECT jsonb_object_agg(e.key, coalesce(c.role, 'todo'))
    FROM jsonb_each_text(i.status_map) e
    JOIN board_columns c ON c.id::text = e.value
), '{}'::jsonb);
ALTER TABLE tasks ADD COLUMN board_column text NOT NULL DEFAULT 'backlog'
    CHECK (board_column IN ('backlog', 'todo', 'in_progress', 'review', 'done'));
UPDATE tasks t SET board_column = coalesce(c.role, 'todo') FROM board_columns c WHERE c.id = t.column_id;
DROP INDEX tasks_board_idx;
ALTER TABLE tasks DROP COLUMN column_id;
CREATE INDEX tasks_board_idx ON tasks (project_id, board_column, position);
DROP TABLE board_columns;
