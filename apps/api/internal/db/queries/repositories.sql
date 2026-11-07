-- name: CreateRepository :one
INSERT INTO repositories (id, project_id, name, role, source_type, local_path, remote_url, default_branch, settings)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListRepositories :many
SELECT * FROM repositories WHERE project_id = $1 ORDER BY name;

-- name: GetRepository :one
SELECT * FROM repositories WHERE id = $1;

-- name: UpdateRepository :one
UPDATE repositories SET
    name           = coalesce(sqlc.narg(name), name),
    role           = coalesce(sqlc.narg(role), role),
    remote_url     = coalesce(sqlc.narg(remote_url), remote_url),
    default_branch = coalesce(sqlc.narg(default_branch), default_branch),
    settings       = coalesce(sqlc.narg(settings), settings),
    updated_at     = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateRepositoryGitInfo :one
UPDATE repositories SET
    default_branch   = coalesce(default_branch, sqlc.narg(detected_default_branch)),
    current_branch   = sqlc.narg(current_branch),
    head_sha         = sqlc.narg(head_sha),
    git_remotes      = sqlc.arg(git_remotes),
    git_error        = sqlc.narg(git_error),
    git_inspected_at = now(),
    updated_at       = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: QueueRepositoryIndex :one
UPDATE repositories SET
    index_status       = 'queued',
    index_requested_at = now(),
    updated_at         = now()
WHERE id = $1
RETURNING *;

-- name: DeleteRepository :one
DELETE FROM repositories WHERE id = $1 RETURNING *;

-- name: CreateRepositoryRelationship :one
INSERT INTO repository_relationships (id, project_id, from_repository_id, to_repository_id, kind, note)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListRepositoryRelationships :many
SELECT * FROM repository_relationships WHERE project_id = $1 ORDER BY created_at;

-- name: DeleteRepositoryRelationship :one
DELETE FROM repository_relationships WHERE id = $1 RETURNING *;
