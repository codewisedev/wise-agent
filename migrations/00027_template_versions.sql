-- +goose Up
-- Every save of a workflow template keeps the definition it replaced, so a
-- bad edit (yours or an applied improvement) can be restored.
CREATE TABLE workflow_template_versions (
    template_id uuid NOT NULL REFERENCES workflow_templates (id) ON DELETE CASCADE,
    version     int NOT NULL,
    name        text NOT NULL,
    description text NOT NULL,
    definition  jsonb NOT NULL,
    changed_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version)
);

-- +goose Down
DROP TABLE workflow_template_versions;
