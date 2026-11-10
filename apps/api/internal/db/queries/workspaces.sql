-- name: InsertWorkspace :one
INSERT INTO workspaces (id, task_id, workflow_run_id, repository_id, branch, base_branch, image, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: LiveWorkspaceForTask :one
SELECT * FROM workspaces
WHERE task_id = $1 AND status IN ('preparing', 'ready', 'stopped')
ORDER BY created_at DESC LIMIT 1;

-- name: ListTaskWorkspaces :many
SELECT * FROM workspaces WHERE task_id = $1 ORDER BY created_at DESC LIMIT 20;

-- name: SetWorkspaceJob :exec
UPDATE workspaces SET job_id = $2, status = 'preparing', error = NULL, updated_at = now() WHERE id = $1;

-- name: AppendWorkspaceLog :exec
-- Keeps the last 64 KB of the preparation log.
UPDATE workspaces SET log = right(log || sqlc.arg(chunk)::text, 65536), updated_at = now() WHERE id = $1;

-- name: SetWorkspaceReady :one
UPDATE workspaces
SET status = 'ready', base_branch = $2, base_sha = $3, tool_versions = $4, scrubbed = $5, error = NULL, updated_at = now()
WHERE id = $1 AND status = 'preparing' RETURNING *;

-- name: SetWorkspaceFailed :one
-- Only a preparing workspace fails (a concurrent removal wins).
UPDATE workspaces SET status = 'failed', error = $2, updated_at = now(), finished_at = now()
WHERE id = $1 AND status = 'preparing' RETURNING *;

-- name: ListPreparingWorkspaces :many
SELECT * FROM workspaces WHERE status = 'preparing' AND job_id IS NOT NULL AND updated_at < now() - interval '1 minute' LIMIT 50;

-- name: SetWorkspaceStatus :one
UPDATE workspaces SET status = $2, updated_at = now(),
    finished_at = CASE WHEN $2 IN ('removed', 'failed') THEN now() ELSE finished_at END
WHERE id = $1 RETURNING *;

-- name: SetWorkspaceHead :exec
UPDATE workspaces SET head_sha = $2, updated_at = now() WHERE id = $1;

-- name: SetWorkspaceDelivered :one
UPDATE workspaces SET delivered_branch = $2, delivered_sha = $3, delivered_at = now(), updated_at = now()
WHERE id = $1 RETURNING *;

-- name: ListExpiredWorkspaces :many
-- Workspaces to clean up: their task is finished (or the workspace failed)
-- and they have not changed for the retention period.
SELECT w.* FROM workspaces w JOIN tasks t ON t.id = w.task_id
WHERE w.status IN ('ready', 'stopped', 'failed')
  AND w.updated_at < now() - make_interval(days => sqlc.arg(days)::int)
  AND (w.status = 'failed' OR t.status IN ('completed', 'failed', 'cancelled') OR t.archived_at IS NOT NULL)
LIMIT 50;

-- name: SetTaskTargetRepository :exec
UPDATE tasks SET target_repository_id = $2, updated_at = now() WHERE id = $1;

-- name: SetRepositorySettings :one
UPDATE repositories SET settings = $2, updated_at = now() WHERE id = $1 RETURNING *;
