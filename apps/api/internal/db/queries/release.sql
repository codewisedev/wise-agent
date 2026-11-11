-- name: SetRepositoryRelease :one
UPDATE repositories SET release = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetProjectReleasePolicy :one
UPDATE projects SET release_policy = $2, updated_at = now() WHERE id = $1 RETURNING release_policy;

-- name: GetProjectReleasePolicy :one
SELECT release_policy FROM projects WHERE id = $1;
