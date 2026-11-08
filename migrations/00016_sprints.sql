-- +goose Up
-- Sprints: a project's time boxes. Tasks may belong to one; sources may feed a
-- fixed sprint, or follow the source's own sprints (ClickUp sprint folder lists,
-- Jira board sprints), in which case Wise creates and updates those sprints.
CREATE TABLE sprints (
    id             uuid PRIMARY KEY,
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    starts_on      date,
    ends_on        date,
    closed         boolean NOT NULL DEFAULT false,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL, -- followed from this source
    external_id    text,                                                -- the source's list / sprint id
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on)
);
CREATE INDEX sprints_project_idx ON sprints (project_id, starts_on);
CREATE UNIQUE INDEX sprints_external_idx ON sprints (integration_id, external_id) WHERE integration_id IS NOT NULL;

-- The sprint Wise treats as current (maintained by Wise; see tasks.RefreshSprints).
ALTER TABLE projects ADD COLUMN current_sprint_id uuid REFERENCES sprints (id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN sprint_id uuid REFERENCES sprints (id) ON DELETE SET NULL;
CREATE INDEX tasks_sprint_idx ON tasks (sprint_id);
-- A source can feed one fixed sprint (follow mode is in its config).
ALTER TABLE integrations ADD COLUMN sprint_id uuid REFERENCES sprints (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE integrations DROP COLUMN sprint_id;
DROP INDEX tasks_sprint_idx;
ALTER TABLE tasks DROP COLUMN sprint_id;
ALTER TABLE projects DROP COLUMN current_sprint_id;
DROP TABLE sprints;
