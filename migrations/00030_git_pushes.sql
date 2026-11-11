-- +goose Up
-- Pushes and pull requests made by the trusted git worker. A push exists
-- only for an approved push approval (one per approval); a pull request only
-- for a successful push.
CREATE TABLE git_pushes (
    id            uuid PRIMARY KEY,
    approval_id   uuid NOT NULL UNIQUE REFERENCES approvals (id) ON DELETE CASCADE,
    task_id       uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    workflow_run_id uuid REFERENCES workflow_runs (id) ON DELETE SET NULL,
    repository_id uuid REFERENCES repositories (id) ON DELETE SET NULL,
    account_id    uuid REFERENCES accounts (id) ON DELETE SET NULL,
    workspace_id  uuid NOT NULL,
    remote        text NOT NULL,
    branch        text NOT NULL CHECK (branch LIKE 'wise/%'),
    sha           text NOT NULL CHECK (sha ~ '^[0-9a-f]{40,64}$'),
    base_branch   text NOT NULL,
    status        text NOT NULL DEFAULT 'granted' CHECK (status IN ('granted', 'pushed', 'failed')),
    attempts      int NOT NULL DEFAULT 1,
    existed       boolean NOT NULL DEFAULT false,
    error         text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    pushed_at     timestamptz
);
CREATE INDEX git_pushes_task_idx ON git_pushes (task_id, created_at DESC);

CREATE TABLE git_pull_requests (
    id           uuid PRIMARY KEY,
    push_id      uuid NOT NULL UNIQUE REFERENCES git_pushes (id) ON DELETE CASCADE,
    approval_id  uuid REFERENCES approvals (id) ON DELETE SET NULL, -- when the policy asks
    status       text NOT NULL DEFAULT 'granted' CHECK (status IN ('granted', 'opened', 'failed')),
    number       int,
    url          text,
    state        text, -- open, merged, closed (from the host)
    draft        boolean NOT NULL DEFAULT true,
    title        text NOT NULL DEFAULT '',
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    refreshed_at timestamptz
);

-- +goose Down
DROP TABLE git_pull_requests;
DROP TABLE git_pushes;
