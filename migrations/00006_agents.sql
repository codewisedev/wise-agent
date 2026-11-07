-- +goose Up
-- One row per distinct definition content; a changed definition or prompt creates a new row.
CREATE TABLE agent_definitions (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    version      text NOT NULL,
    content_hash text NOT NULL UNIQUE,
    definition   jsonb NOT NULL,
    prompt       text NOT NULL DEFAULT '',
    enabled      boolean NOT NULL,
    is_current   boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX agent_definitions_current_idx ON agent_definitions (name) WHERE is_current;

CREATE TYPE agent_run_status AS ENUM ('queued', 'running', 'succeeded', 'failed', 'cancelled');

CREATE TABLE agent_runs (
    id                  uuid PRIMARY KEY,
    agent_definition_id uuid NOT NULL REFERENCES agent_definitions (id),
    agent_name          text NOT NULL,
    project_id          uuid REFERENCES projects (id) ON DELETE SET NULL,
    status              agent_run_status NOT NULL DEFAULT 'queued',
    input               jsonb NOT NULL,
    output              jsonb,
    error               text,
    attempt             int NOT NULL DEFAULT 0,
    max_attempts        int NOT NULL,
    runtime_id          text,
    run_token_hash      bytea,
    lease_expires_at    timestamptz,
    cancel_requested    boolean NOT NULL DEFAULT false,
    total_input_tokens  bigint NOT NULL DEFAULT 0,
    total_output_tokens bigint NOT NULL DEFAULT 0,
    total_cost_usd      numeric(12, 6) NOT NULL DEFAULT 0,
    created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_runs_queue_idx ON agent_runs (created_at) WHERE status = 'queued';
CREATE INDEX agent_runs_lease_idx ON agent_runs (lease_expires_at) WHERE status = 'running';
CREATE INDEX agent_runs_agent_idx ON agent_runs (agent_name, created_at DESC);
CREATE INDEX agent_runs_project_idx ON agent_runs (project_id, created_at DESC);
CREATE UNIQUE INDEX agent_runs_token_idx ON agent_runs (run_token_hash) WHERE run_token_hash IS NOT NULL;

CREATE TABLE agent_events (
    id         bigserial PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    seq        int NOT NULL,
    type       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, seq)
);

CREATE TABLE tool_calls (
    id          uuid PRIMARY KEY,
    run_id      uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    tool        text NOT NULL,
    input       jsonb NOT NULL DEFAULT '{}',
    output      jsonb,
    status      text NOT NULL CHECK (status IN ('ok', 'error')),
    error       text,
    duration_ms int NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tool_calls_run_idx ON tool_calls (run_id, created_at);

-- +goose Down
DROP TABLE tool_calls;
DROP TABLE agent_events;
DROP TABLE agent_runs;
DROP TYPE agent_run_status;
DROP TABLE agent_definitions;
