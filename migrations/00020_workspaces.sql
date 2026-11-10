-- +goose Up
-- Task workspaces: a private clone of one repository, a sandbox for it, and
-- the branch delivered into the user's repository after final approval.
ALTER TABLE tasks ADD COLUMN target_repository_id uuid REFERENCES repositories (id) ON DELETE SET NULL;

CREATE TABLE workspaces (
    id              uuid PRIMARY KEY,
    task_id         uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    workflow_run_id uuid REFERENCES workflow_runs (id) ON DELETE SET NULL,
    repository_id   uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    status          text NOT NULL DEFAULT 'preparing'
                    CHECK (status IN ('preparing', 'ready', 'failed', 'stopped', 'removed')),
    branch          text NOT NULL,
    base_branch     text,
    base_sha        text,
    image           text NOT NULL,
    job_id          text,
    log             text NOT NULL DEFAULT '',
    error           text,
    tool_versions   text[] NOT NULL DEFAULT '{}',
    scrubbed        text[] NOT NULL DEFAULT '{}',
    head_sha        text,
    delivered_branch text,
    delivered_sha   text,
    delivered_at    timestamptz,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX workspaces_task_idx ON workspaces (task_id, created_at DESC);
CREATE INDEX workspaces_status_idx ON workspaces (status, updated_at);
-- One live workspace per task and repository.
CREATE UNIQUE INDEX workspaces_live_idx ON workspaces (task_id, repository_id) WHERE status IN ('preparing', 'ready', 'stopped');

-- +goose Down
DROP TABLE workspaces;
ALTER TABLE tasks DROP COLUMN target_repository_id;
