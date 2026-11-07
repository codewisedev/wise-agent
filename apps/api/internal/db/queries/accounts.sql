-- name: CreateAccount :one
INSERT INTO accounts (id, name, kind, base_url, default_model, cloud, enabled, secret_ciphertext, secret_hint, metadata, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: GetAccountByName :one
SELECT * FROM accounts WHERE name = $1;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY kind, name;

-- name: UpdateAccount :one
UPDATE accounts SET
    name          = sqlc.arg(name),
    base_url      = sqlc.arg(base_url),
    default_model = sqlc.arg(default_model),
    cloud         = sqlc.arg(cloud),
    enabled       = sqlc.arg(enabled),
    metadata      = sqlc.arg(metadata),
    updated_at    = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetAccountSecret :exec
UPDATE accounts SET secret_ciphertext = $2, secret_hint = $3, updated_at = now() WHERE id = $1;

-- name: DeleteAccount :execrows
DELETE FROM accounts WHERE id = $1;
