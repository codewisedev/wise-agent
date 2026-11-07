-- +goose Up
-- Phase 3b: task sources. An integration imports issues/cards from Jira, ClickUp
-- or Trello into a project's board (import only; Wise never writes back).
CREATE TABLE integrations (
    id                  uuid PRIMARY KEY,
    project_id          uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('jira', 'clickup', 'trello')),
    name                text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    account_id          uuid NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    config              jsonb NOT NULL DEFAULT '{}',   -- jql | listId | boardId (+ list filter)
    status_map          jsonb NOT NULL DEFAULT '{}',   -- external status (lowercase) -> board column
    default_template_id uuid REFERENCES workflow_templates (id) ON DELETE SET NULL,
    auto_start          boolean NOT NULL DEFAULT false,
    interval_minutes    int NOT NULL DEFAULT 15 CHECK (interval_minutes BETWEEN 5 AND 1440),
    enabled             boolean NOT NULL DEFAULT true,
    last_synced_at      timestamptz,
    last_result         jsonb,
    last_error          text,
    sync_started_at     timestamptz,                   -- lease: one sync at a time
    created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX integrations_project_idx ON integrations (project_id);

ALTER TABLE tasks ADD CONSTRAINT tasks_integration_fk
    FOREIGN KEY (integration_id) REFERENCES integrations (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE tasks DROP CONSTRAINT tasks_integration_fk;
DROP TABLE integrations;
