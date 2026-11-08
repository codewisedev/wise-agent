-- name: ListSprints :many
SELECT s.*,
       (SELECT count(*) FROM tasks t WHERE t.sprint_id = s.id AND t.archived_at IS NULL) AS task_count,
       (SELECT count(*) FROM tasks t LEFT JOIN board_columns c ON c.id = t.column_id
         WHERE t.sprint_id = s.id AND t.archived_at IS NULL AND coalesce(c.role, '') <> 'done') AS open_count,
       (p.current_sprint_id IS NOT DISTINCT FROM s.id) AS is_current,
       i.name AS source_name
FROM sprints s
JOIN projects p ON p.id = s.project_id
LEFT JOIN integrations i ON i.id = s.integration_id
WHERE s.project_id = $1
ORDER BY s.closed, coalesce(s.starts_on, s.created_at::date) DESC, s.created_at DESC;

-- name: GetSprint :one
SELECT * FROM sprints WHERE id = $1;

-- name: InsertSprint :one
INSERT INTO sprints (id, project_id, name, starts_on, ends_on, integration_id, external_id, closed)
VALUES ($1, $2, $3, sqlc.narg(starts_on), sqlc.narg(ends_on), sqlc.narg(integration_id), sqlc.narg(external_id), sqlc.arg(closed))
RETURNING *;

-- name: UpdateSprint :one
UPDATE sprints SET name = $2, starts_on = sqlc.narg(starts_on), ends_on = sqlc.narg(ends_on), closed = sqlc.arg(closed), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetSprintByExternal :one
SELECT * FROM sprints WHERE integration_id = $1 AND external_id = $2;

-- name: DeleteSprint :exec
DELETE FROM sprints WHERE id = $1;

-- name: SetCurrentSprint :exec
UPDATE projects SET current_sprint_id = sqlc.narg(sprint_id), updated_at = now() WHERE id = sqlc.arg(project_id);

-- name: GetCurrentSprintID :one
SELECT current_sprint_id FROM projects WHERE id = $1;

-- name: CarryOverTasks :execrows
-- Unfinished tasks created in Wise move from one sprint to the next; imported
-- tasks follow their source instead.
UPDATE tasks t SET sprint_id = sqlc.narg(to_sprint), updated_at = now()
FROM board_columns c
WHERE c.id = t.column_id AND t.sprint_id = sqlc.arg(from_sprint) AND t.source = 'manual'
  AND coalesce(c.role, '') <> 'done';

-- name: SetTaskSprint :exec
UPDATE tasks SET sprint_id = sqlc.narg(sprint_id), updated_at = now() WHERE id = sqlc.arg(id);

-- name: ListProjectsWithSprints :many
SELECT DISTINCT project_id FROM sprints WHERE NOT closed;
