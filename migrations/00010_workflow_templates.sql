-- +goose Up
-- Phase 3b: configurable workflows. A template is an ordered list of agent steps
-- with optional review gates; a task picks a template and may override steps.
CREATE TABLE workflow_templates (
    id          uuid PRIMARY KEY,
    slug        text UNIQUE,                 -- set for templates Wise seeds and updates itself
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description text NOT NULL DEFAULT '',
    definition  jsonb NOT NULL,
    version     int NOT NULL DEFAULT 1,
    is_default  boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX workflow_templates_default_idx ON workflow_templates (is_default) WHERE is_default;

ALTER TABLE tasks
    ADD COLUMN template_id        uuid REFERENCES workflow_templates (id) ON DELETE SET NULL,
    ADD COLUMN workflow_overrides jsonb NOT NULL DEFAULT '{}';

-- Steps and artifact kinds are now defined by templates and agents.
ALTER TABLE artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_kind_check CHECK (kind ~ '^[a-z][a-z0-9_]{1,62}$');
ALTER TABLE approvals DROP CONSTRAINT approvals_gate_check;
ALTER TABLE approvals ADD CONSTRAINT approvals_gate_check CHECK (gate ~ '^[a-z][a-z0-9_-]{0,40}$');

-- Phase 3 runs used the fixed EngineeringWorkflow, which no worker serves any
-- more: close any still active so their tasks can run the new workflows.
UPDATE approvals SET status = 'cancelled'
WHERE status = 'pending' AND workflow_run_id IN (
    SELECT id FROM workflow_runs
    WHERE workflow_type = 'EngineeringWorkflow' AND status IN ('pending', 'running', 'awaiting_approval', 'awaiting_retry'));
UPDATE workflow_commands SET processed_at = now(), last_error = 'superseded by configurable workflows'
WHERE processed_at IS NULL AND workflow_run_id IN (SELECT id FROM workflow_runs WHERE workflow_type = 'EngineeringWorkflow');
UPDATE tasks SET status = 'cancelled', updated_at = now()
WHERE id IN (
    SELECT task_id FROM workflow_runs
    WHERE workflow_type = 'EngineeringWorkflow' AND status IN ('pending', 'running', 'awaiting_approval', 'awaiting_retry'));
UPDATE workflow_runs SET status = 'cancelled', finished_at = now(), updated_at = now(),
    error = 'stopped by the upgrade to configurable workflows; run the task again'
WHERE workflow_type = 'EngineeringWorkflow' AND status IN ('pending', 'running', 'awaiting_approval', 'awaiting_retry');

-- +goose Down
ALTER TABLE approvals DROP CONSTRAINT approvals_gate_check;
-- NOT VALID: rows created by configurable workflows stay; new rows are checked.
ALTER TABLE approvals ADD CONSTRAINT approvals_gate_check CHECK (gate IN ('scope', 'plan')) NOT VALID;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_kind_check;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_kind_check
    CHECK (kind IN ('product_analysis', 'requirements', 'plan', 'architecture_review', 'final_report')) NOT VALID;
ALTER TABLE tasks DROP COLUMN workflow_overrides, DROP COLUMN template_id;
DROP TABLE workflow_templates;
