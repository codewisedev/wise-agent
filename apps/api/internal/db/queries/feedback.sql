-- name: UpsertFeedback :one
INSERT INTO feedback (id, target_type, target_id, run_id, artifact_id, agent_name, agent_definition_id, task_id, rating, note, user_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (user_id, target_type, target_id) DO UPDATE
SET rating = EXCLUDED.rating, note = EXCLUDED.note, updated_at = now()
RETURNING *;

-- name: DeleteFeedback :one
DELETE FROM feedback WHERE user_id = $1 AND target_type = $2 AND target_id = $3 RETURNING id;

-- name: ListFeedback :many
SELECT f.*, u.username FROM feedback f JOIN users u ON u.id = f.user_id
WHERE (sqlc.narg(target_type)::text IS NULL OR f.target_type = sqlc.narg(target_type))
  AND (sqlc.narg(target_id)::uuid IS NULL OR f.target_id = sqlc.narg(target_id))
  AND (sqlc.narg(agent_name)::text IS NULL OR f.agent_name = sqlc.narg(agent_name))
  AND (sqlc.narg(rating)::text IS NULL OR f.rating = sqlc.narg(rating))
  AND (sqlc.narg(task_id)::uuid IS NULL OR f.task_id = sqlc.narg(task_id))
ORDER BY f.updated_at DESC
LIMIT sqlc.arg(row_limit);
