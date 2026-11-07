-- name: CreateIntegration :one
INSERT INTO integrations (id, project_id, kind, name, account_id, config, status_map, default_template_id,
                          auto_start, interval_minutes, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetIntegration :one
SELECT * FROM integrations WHERE id = $1;

-- name: ListIntegrations :many
SELECT * FROM integrations WHERE project_id = $1 ORDER BY created_at;

-- name: UpdateIntegration :one
UPDATE integrations SET
    name = $2, account_id = $3, config = $4, status_map = $5, default_template_id = $6,
    auto_start = $7, interval_minutes = $8, enabled = $9, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteIntegration :execrows
DELETE FROM integrations WHERE id = $1;

-- Lease an integration for one sync; a lease older than 10 minutes is considered dead.
-- name: LeaseIntegrationSync :one
UPDATE integrations SET sync_started_at = now()
WHERE id = $1 AND (sync_started_at IS NULL OR sync_started_at < now() - interval '10 minutes')
RETURNING *;

-- name: FinishIntegrationSync :exec
UPDATE integrations SET sync_started_at = NULL, last_synced_at = now(), last_result = $2, last_error = $3
WHERE id = $1;

-- name: ListDueIntegrations :many
SELECT i.id FROM integrations i
JOIN projects p ON p.id = i.project_id AND p.archived_at IS NULL
WHERE i.enabled AND (i.last_synced_at IS NULL OR i.last_synced_at < now() - make_interval(mins => i.interval_minutes))
ORDER BY i.last_synced_at NULLS FIRST
LIMIT 20;

-- name: GetTaskByExternal :one
SELECT * FROM tasks WHERE integration_id = $1 AND external_id = $2;

-- name: CreateImportedTask :one
INSERT INTO tasks (id, project_id, title, description, board_column, position, priority, labels, source,
                   integration_id, external_id, external_url, external_status, template_id, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
RETURNING *;

-- name: UpdateImportedTask :one
UPDATE tasks SET
    title = $2, description = $3, labels = $4, priority = $5, external_url = $6, external_status = $7,
    board_column = $8, position = $9, synced_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;
