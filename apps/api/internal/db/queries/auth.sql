-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateUser :one
INSERT INTO users (id, username, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: CreateSession :one
INSERT INTO sessions (id, user_id, csrf_token, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetActiveSession :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.id = $1
  AND sessions.expires_at > now()
  AND users.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now(), expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();

-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, user_id, name, prefix, token_hash, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetActiveAPITokenByPrefix :one
SELECT sqlc.embed(api_tokens), sqlc.embed(users)
FROM api_tokens
JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.prefix = $1
  AND api_tokens.revoked_at IS NULL
  AND (api_tokens.expires_at IS NULL OR api_tokens.expires_at > now())
  AND users.disabled_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;
