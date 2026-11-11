-- +goose Up
-- Why a run exists: real work, or an evaluation replay (excluded from
-- metrics, budgets and memory extraction).
ALTER TABLE agent_runs ADD COLUMN purpose text NOT NULL DEFAULT 'work' CHECK (purpose IN ('work', 'eval'));
CREATE INDEX agent_runs_insights_idx ON agent_runs (created_at, agent_name) WHERE purpose = 'work';

-- +goose Down
DROP INDEX agent_runs_insights_idx;
ALTER TABLE agent_runs DROP COLUMN purpose;
