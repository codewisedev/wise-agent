-- name: CreateProject :one
INSERT INTO projects (id, slug, name, description, metadata)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListProjects :many
SELECT sqlc.embed(p),
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id) AS repository_count
FROM projects p
WHERE sqlc.arg(include_archived)::boolean OR p.archived_at IS NULL
ORDER BY p.name;

-- name: GetProject :one
SELECT sqlc.embed(p),
       (SELECT count(*) FROM repositories r WHERE r.project_id = p.id) AS repository_count
FROM projects p
WHERE p.id = $1;

-- name: LockActiveProject :one
SELECT id FROM projects WHERE id = $1 AND archived_at IS NULL FOR UPDATE;

-- name: UpdateProject :one
UPDATE projects SET
    name        = coalesce(sqlc.narg(name), name),
    description = coalesce(sqlc.narg(description), description),
    metadata    = coalesce(sqlc.narg(metadata), metadata),
    updated_at  = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ArchiveProject :execrows
UPDATE projects SET archived_at = now(), updated_at = now()
WHERE id = $1 AND archived_at IS NULL;

-- name: CreateProjectResource :one
INSERT INTO project_resources (id, project_id, kind, name, uri, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListProjectResources :many
SELECT * FROM project_resources WHERE project_id = $1 ORDER BY kind, name;

-- name: DeleteProjectResource :one
DELETE FROM project_resources WHERE id = $1 RETURNING *;

-- name: SetProjectCodingRuntimes :exec
UPDATE projects SET coding_runtimes = $2, updated_at = now() WHERE id = $1;
