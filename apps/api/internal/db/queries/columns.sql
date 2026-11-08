-- name: ListBoardColumns :many
SELECT * FROM board_columns WHERE project_id = $1 ORDER BY position, created_at;

-- name: GetBoardColumn :one
SELECT * FROM board_columns WHERE id = $1;

-- name: LockBoardColumns :many
SELECT * FROM board_columns WHERE project_id = $1 ORDER BY position, created_at FOR UPDATE;

-- name: GetColumnByRole :one
SELECT * FROM board_columns WHERE project_id = $1 AND role = $2;

-- name: InsertBoardColumn :one
INSERT INTO board_columns (id, project_id, name, position, role, color, wip_limit)
VALUES ($1, $2, $3, $4, sqlc.narg(role), sqlc.narg(color), sqlc.narg(wip_limit))
RETURNING *;

-- name: UpdateBoardColumn :one
UPDATE board_columns SET name = $2, position = $3, role = sqlc.narg(role), color = sqlc.narg(color),
    wip_limit = sqlc.narg(wip_limit), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClearColumnRole :exec
UPDATE board_columns SET role = NULL, updated_at = now() WHERE project_id = $1 AND role = $2 AND id <> $3;

-- name: DeleteBoardColumn :exec
DELETE FROM board_columns WHERE id = $1;

-- name: NextColumnPosition :one
SELECT (coalesce(max(position), 0) + 1)::double precision FROM board_columns WHERE project_id = $1;

-- name: CountColumnCards :one
SELECT count(*) FROM tasks WHERE column_id = $1;

-- name: MoveColumnCards :exec
UPDATE tasks SET column_id = sqlc.arg(to_column),
    position = sqlc.arg(base)::double precision + position, updated_at = now()
WHERE column_id = sqlc.arg(from_column);

-- name: RemapIntegrationColumn :exec
-- Status-map entries pointing at a removed column follow its cards (or are dropped).
UPDATE integrations SET status_map = coalesce((
    SELECT jsonb_object_agg(e.key, CASE WHEN e.value = sqlc.arg(from_column)::text THEN sqlc.narg(to_column)::text ELSE e.value END)
    FROM jsonb_each_text(status_map) e
    WHERE e.value <> sqlc.arg(from_column)::text OR sqlc.narg(to_column)::text IS NOT NULL
), '{}'::jsonb), updated_at = now()
WHERE project_id = sqlc.arg(project_id) AND status_map::text LIKE '%' || sqlc.arg(from_column)::text || '%';
