-- name: GetSetting :one
SELECT * FROM app_settings WHERE key = $1;

-- name: LockSetting :one
SELECT * FROM app_settings WHERE key = $1 FOR UPDATE;

-- name: ListSettings :many
SELECT * FROM app_settings ORDER BY key;

-- name: InsertSetting :exec
INSERT INTO app_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING;

-- name: UpdateSetting :one
UPDATE app_settings SET value = $2, version = version + 1, updated_by = $3, updated_at = now()
WHERE key = $1
RETURNING *;

-- name: InsertSettingHistory :exec
INSERT INTO app_settings_history (key, version, value, updated_by) VALUES ($1, $2, $3, $4);

-- name: ListSettingHistory :many
SELECT * FROM app_settings_history WHERE key = $1 ORDER BY version DESC LIMIT 50;
