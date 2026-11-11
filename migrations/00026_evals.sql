-- +goose Up
-- Replay evaluation (implementation-plan §13): a proposal is compared with the
-- current version on cases built from past runs, and scored by contract
-- validity, deterministic checks and a judge agent.

-- A candidate is a definition version created only to be evaluated: never
-- current, hidden from version lists and restore until it is applied.
ALTER TABLE agent_definitions ADD COLUMN candidate boolean NOT NULL DEFAULT false;

CREATE TABLE eval_cases (
    id                 uuid PRIMARY KEY,
    agent_name         text NOT NULL,
    kind               text NOT NULL CHECK (kind IN ('golden', 'must_fix', 'custom')),
    input              jsonb NOT NULL,
    project_id         uuid REFERENCES projects (id) ON DELETE CASCADE,
    source_run_id      uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    source             text NOT NULL DEFAULT '', -- approval, feedback or manual
    expectation        text NOT NULL DEFAULT '' CHECK (length(expectation) <= 4000),
    enabled            boolean NOT NULL DEFAULT true,
    created_by         uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX eval_cases_source_idx ON eval_cases (source_run_id) WHERE source_run_id IS NOT NULL;
CREATE INDEX eval_cases_agent_idx ON eval_cases (agent_name, created_at DESC);

CREATE TABLE eval_runs (
    id                      uuid PRIMARY KEY,
    proposal_id             uuid NOT NULL REFERENCES improvement_proposals (id) ON DELETE CASCADE,
    agent_name              text NOT NULL,
    baseline_definition_id  uuid NOT NULL REFERENCES agent_definitions (id),
    candidate_definition_id uuid NOT NULL REFERENCES agent_definitions (id),
    baseline_model          jsonb,  -- {account, model} pinned for baseline runs (null = routing)
    candidate_model         jsonb,
    judge_model             jsonb,
    status                  text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    cost_cap_usd            float8 NOT NULL CHECK (cost_cap_usd > 0),
    summary                 jsonb,
    error                   text,
    created_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    finished_at             timestamptz
);
CREATE UNIQUE INDEX eval_runs_active_idx ON eval_runs (proposal_id) WHERE status = 'running';

CREATE TABLE eval_results (
    id                uuid PRIMARY KEY,
    eval_run_id       uuid NOT NULL REFERENCES eval_runs (id) ON DELETE CASCADE,
    case_id           uuid REFERENCES eval_cases (id) ON DELETE SET NULL,
    case_kind         text NOT NULL,
    expectation       text NOT NULL DEFAULT '',
    baseline_run_id   uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    candidate_run_id  uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    judge_run_id      uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    status            text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'judging', 'done', 'failed')),
    checks            jsonb,          -- per side: contract validity, invalid attempts, empty fields, secrets
    a_is              text CHECK (a_is IN ('baseline', 'candidate')),
    baseline_score    int,
    candidate_score   int,
    reasoning         text,
    error             text,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX eval_results_run_idx ON eval_results (eval_run_id);
CREATE INDEX eval_results_baseline_idx ON eval_results (baseline_run_id);
CREATE INDEX eval_results_candidate_idx ON eval_results (candidate_run_id);
CREATE INDEX eval_results_judge_idx ON eval_results (judge_run_id);

-- +goose Down
DROP TABLE eval_results;
DROP TABLE eval_runs;
DROP TABLE eval_cases;
ALTER TABLE agent_definitions DROP COLUMN candidate;
