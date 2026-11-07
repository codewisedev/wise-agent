-- +goose Up
-- Phase 3: tasks, durable engineering workflows (Temporal), versioned artifacts and approvals.
-- Temporal owns workflow execution; these tables are Wise's queryable mirror plus the
-- outbox (workflow_commands) the API uses to talk to the orchestrator.

CREATE TABLE tasks (
    id          uuid PRIMARY KEY,
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'open'
                CHECK (status IN ('open', 'in_progress', 'awaiting_approval', 'completed', 'failed', 'cancelled')),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tasks_project_idx ON tasks (project_id, created_at DESC);
CREATE INDEX tasks_status_idx ON tasks (status, created_at DESC);

CREATE TABLE workflow_runs (
    id              uuid PRIMARY KEY,
    task_id         uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    workflow_type   text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'awaiting_approval', 'awaiting_retry',
                                      'completed', 'failed', 'cancelled')),
    current_stage   text,
    error           text,
    config          jsonb NOT NULL DEFAULT '{}',
    temporal_run_id text,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workflow_runs_task_idx ON workflow_runs (task_id, created_at DESC);
-- At most one active workflow per task.
CREATE UNIQUE INDEX workflow_runs_active_idx ON workflow_runs (task_id)
    WHERE status IN ('pending', 'running', 'awaiting_approval', 'awaiting_retry');

CREATE TABLE artifacts (
    id               uuid PRIMARY KEY,
    task_id          uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    workflow_run_id  uuid REFERENCES workflow_runs (id) ON DELETE SET NULL,
    kind             text NOT NULL
                     CHECK (kind IN ('product_analysis', 'requirements', 'plan', 'architecture_review', 'final_report')),
    version          int NOT NULL CHECK (version > 0),
    content          jsonb NOT NULL,
    content_hash     text NOT NULL,
    agent_run_id     uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_id, kind, version)
);

CREATE TABLE workflow_steps (
    id              uuid PRIMARY KEY,
    workflow_run_id uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    stage           text NOT NULL,
    attempt         int NOT NULL DEFAULT 1,
    status          text NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'awaiting_approval', 'succeeded', 'failed', 'cancelled', 'skipped')),
    agent_run_id    uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    artifact_id     uuid REFERENCES artifacts (id) ON DELETE SET NULL,
    error           text,
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    UNIQUE (workflow_run_id, stage, attempt)
);

CREATE TABLE approvals (
    id              uuid PRIMARY KEY,
    workflow_run_id uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    task_id         uuid NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    gate            text NOT NULL CHECK (gate IN ('scope', 'plan')),
    round           int NOT NULL DEFAULT 1,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'rejected', 'superseded', 'cancelled')),
    -- [{artifactId, kind, version, hash}] — the exact versions under review.
    artifacts       jsonb NOT NULL,
    combined_hash   text NOT NULL,
    notes           jsonb NOT NULL DEFAULT '[]',
    comment         text,
    decided_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at      timestamptz,
    consumed_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_run_id, gate, round)
);
CREATE INDEX approvals_status_idx ON approvals (status, created_at DESC);
CREATE INDEX approvals_artifacts_idx ON approvals USING gin (artifacts jsonb_path_ops);

CREATE TABLE workflow_events (
    id              bigserial PRIMARY KEY,
    workflow_run_id uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    seq             int NOT NULL,
    type            text NOT NULL,
    payload         jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_run_id, seq)
);

-- Transactional outbox: written with the domain change, delivered to Temporal by the orchestrator.
CREATE TABLE workflow_commands (
    id              uuid PRIMARY KEY,
    workflow_run_id uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('start', 'decision', 'retry', 'cancel')),
    payload         jsonb NOT NULL DEFAULT '{}',
    attempts        int NOT NULL DEFAULT 0,
    last_error      text,
    available_at    timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workflow_commands_pending_idx ON workflow_commands (available_at) WHERE processed_at IS NULL;

ALTER TABLE agent_runs
    ADD COLUMN workflow_run_id uuid REFERENCES workflow_runs (id) ON DELETE SET NULL,
    ADD COLUMN idempotency_key text;
CREATE UNIQUE INDEX agent_runs_idempotency_idx ON agent_runs (idempotency_key) WHERE idempotency_key IS NOT NULL;

-- +goose Down
DROP INDEX agent_runs_idempotency_idx;
ALTER TABLE agent_runs DROP COLUMN idempotency_key, DROP COLUMN workflow_run_id;
DROP TABLE workflow_commands;
DROP TABLE workflow_events;
DROP TABLE approvals;
DROP TABLE workflow_steps;
DROP TABLE artifacts;
DROP TABLE workflow_runs;
DROP TABLE tasks;
