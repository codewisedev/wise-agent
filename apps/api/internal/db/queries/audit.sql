-- name: InsertAuditLog :exec
INSERT INTO audit_logs (id, actor_type, actor_id, action, entity_type, entity_id, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditLogsForEntity :many
SELECT * FROM audit_logs
WHERE entity_type = $1 AND entity_id = $2
ORDER BY created_at DESC
LIMIT $3;
