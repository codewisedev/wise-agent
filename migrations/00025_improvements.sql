-- +goose Up
-- Wise's system memory (implementation-plan §12.3, §13): each analysis of its
-- own performance, the proposals it produced, their evidence, and what
-- happened to them (rejected, applied, rolled back).
CREATE TABLE improvement_analyses (
    id           uuid PRIMARY KEY,
    trigger      text NOT NULL CHECK (trigger IN ('manual', 'scheduled')),
    period       text UNIQUE,          -- scheduled runs: one per period (e.g. 2026-W45)
    window_days  int NOT NULL,
    status       text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
    agent_run_id uuid REFERENCES agent_runs (id) ON DELETE SET NULL,
    digest       jsonb NOT NULL,       -- the metrics and signals the analysis saw
    proposals    int NOT NULL DEFAULT 0,
    refused      jsonb NOT NULL DEFAULT '[]', -- proposals the hard rule or the evidence check refused, with reasons
    error        text,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE UNIQUE INDEX improvement_analyses_running_idx ON improvement_analyses ((true)) WHERE status = 'running';

CREATE TABLE improvement_proposals (
    id             uuid PRIMARY KEY,
    analysis_id    uuid REFERENCES improvement_analyses (id) ON DELETE SET NULL,
    kind           text NOT NULL CHECK (kind IN ('prompt', 'memory', 'routing', 'template')),
    target         text NOT NULL,
    title          text NOT NULL,
    rationale      text NOT NULL,
    evidence       jsonb NOT NULL,
    change         jsonb NOT NULL,     -- the typed change (what applying writes)
    before         jsonb NOT NULL,     -- the target as it was when proposed (for the diff)
    base           jsonb NOT NULL,     -- the versions it was written against (staleness)
    fingerprint    text NOT NULL,
    status         text NOT NULL DEFAULT 'proposed'
                   CHECK (status IN ('proposed', 'evaluating', 'evaluated', 'applied', 'rejected', 'rolled_back', 'stale')),
    metrics_before jsonb,
    applied        jsonb,              -- what applying produced and replaced (for rollback)
    decision_note  text,
    decided_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at     timestamptz,
    applied_at     timestamptz,
    rolled_back_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX improvement_proposals_open_idx ON improvement_proposals (fingerprint)
    WHERE status IN ('proposed', 'evaluating', 'evaluated');
CREATE INDEX improvement_proposals_status_idx ON improvement_proposals (status, created_at DESC);

-- +goose Down
DROP TABLE improvement_proposals;
DROP TABLE improvement_analyses;
