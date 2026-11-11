-- +goose Up
-- Memories written by applied improvement proposals are marked, so they never
-- reach the agents behind quality gates.
ALTER TABLE memories DROP CONSTRAINT memories_source_check;
ALTER TABLE memories ADD CONSTRAINT memories_source_check
    CHECK (source IN ('manual', 'review_feedback', 'report', 'profile_inference', 'task', 'improvement'));

-- "applying": claimed by one Apply, so two concurrent applies cannot both write.
ALTER TABLE improvement_proposals DROP CONSTRAINT improvement_proposals_status_check;
ALTER TABLE improvement_proposals ADD CONSTRAINT improvement_proposals_status_check
    CHECK (status IN ('proposed', 'evaluating', 'evaluated', 'applying', 'applied', 'rejected', 'rolled_back', 'stale'));

-- Cases start a few at a time ("pending" until then), so the cost cap stops spending.
ALTER TABLE eval_results DROP CONSTRAINT eval_results_status_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_status_check
    CHECK (status IN ('pending', 'running', 'judging', 'done', 'failed'));
-- One evaluation at a time across all proposals.
DROP INDEX eval_runs_active_idx;
CREATE UNIQUE INDEX eval_runs_active_idx ON eval_runs ((true)) WHERE status = 'running';

-- +goose Down
DROP INDEX eval_runs_active_idx;
CREATE UNIQUE INDEX eval_runs_active_idx ON eval_runs (proposal_id) WHERE status = 'running';
UPDATE eval_results SET status = 'failed' WHERE status = 'pending';
ALTER TABLE eval_results DROP CONSTRAINT eval_results_status_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_status_check CHECK (status IN ('running', 'judging', 'done', 'failed'));
UPDATE improvement_proposals SET status = 'proposed' WHERE status = 'applying';
ALTER TABLE improvement_proposals DROP CONSTRAINT improvement_proposals_status_check;
ALTER TABLE improvement_proposals ADD CONSTRAINT improvement_proposals_status_check
    CHECK (status IN ('proposed', 'evaluating', 'evaluated', 'applied', 'rejected', 'rolled_back', 'stale'));
UPDATE memories SET source = 'manual' WHERE source = 'improvement';
ALTER TABLE memories DROP CONSTRAINT memories_source_check;
ALTER TABLE memories ADD CONSTRAINT memories_source_check
    CHECK (source IN ('manual', 'review_feedback', 'report', 'profile_inference', 'task'));
