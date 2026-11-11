-- name: CreateWorkflowTemplate :one
INSERT INTO workflow_templates (id, slug, name, description, definition, is_default, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetWorkflowTemplate :one
SELECT * FROM workflow_templates WHERE id = $1;

-- name: GetWorkflowTemplateBySlug :one
SELECT * FROM workflow_templates WHERE slug = $1;

-- name: GetDefaultWorkflowTemplate :one
SELECT * FROM workflow_templates WHERE is_default;

-- name: ListWorkflowTemplates :many
SELECT * FROM workflow_templates ORDER BY is_default DESC, name;

-- name: UpdateWorkflowTemplate :one
UPDATE workflow_templates SET
    name = $2, description = $3, definition = $4, version = version + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClearDefaultWorkflowTemplate :exec
UPDATE workflow_templates SET is_default = false WHERE is_default AND id <> $1;

-- name: SetDefaultWorkflowTemplate :exec
UPDATE workflow_templates SET is_default = true WHERE id = $1;

-- name: DeleteWorkflowTemplate :execrows
DELETE FROM workflow_templates WHERE id = $1;

-- name: SetTaskWorkflow :one
UPDATE tasks SET template_id = $2, workflow_overrides = $3, updated_at = now() WHERE id = $1
RETURNING *;

-- name: SetWorkflowRunConfig :exec
UPDATE workflow_runs SET config = $2, updated_at = now() WHERE id = $1;

-- name: LockWorkflowTemplate :one
SELECT * FROM workflow_templates WHERE id = $1 FOR UPDATE;

-- name: InsertWorkflowTemplateVersion :exec
INSERT INTO workflow_template_versions (template_id, version, name, description, definition, changed_by)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (template_id, version) DO NOTHING;

-- name: ListWorkflowTemplateVersions :many
SELECT * FROM workflow_template_versions WHERE template_id = $1 ORDER BY version DESC LIMIT 100;

-- name: GetWorkflowTemplateVersion :one
SELECT * FROM workflow_template_versions WHERE template_id = $1 AND version = $2;
