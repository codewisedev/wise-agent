-- +goose Up
CREATE TABLE model_calls (
    id            uuid PRIMARY KEY,
    run_id        uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    agent_name    text NOT NULL DEFAULT '',
    alias         text NOT NULL,
    provider      text NOT NULL,
    model         text NOT NULL,
    status        text NOT NULL CHECK (status IN ('ok', 'error', 'rejected')),
    error         text,
    input_tokens  int NOT NULL DEFAULT 0,
    output_tokens int NOT NULL DEFAULT 0,
    cost_usd      numeric(12, 6) NOT NULL DEFAULT 0,
    latency_ms    int NOT NULL DEFAULT 0,
    cache_hit     boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_calls_run_idx ON model_calls (run_id, created_at);
CREATE INDEX model_calls_agent_day_idx ON model_calls (agent_name, created_at);

CREATE TABLE model_cache (
    key        bytea PRIMARY KEY,
    response   jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_cache_expires_idx ON model_cache (expires_at);

-- +goose Down
DROP TABLE model_cache;
DROP TABLE model_calls;
