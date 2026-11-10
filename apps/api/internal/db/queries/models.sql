-- name: InsertModelCall :exec
INSERT INTO model_calls (id, run_id, agent_name, alias, provider, model, status, error,
                         input_tokens, output_tokens, cost_usd, latency_ms, cache_hit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: AgentCostSince :one
-- An agent's spend today counts real work only (evaluation replays have their own cap).
SELECT coalesce(sum(mc.cost_usd), 0)::float8 FROM model_calls mc
WHERE mc.agent_name = $1 AND mc.created_at >= $2 AND mc.status = 'ok'
  AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.id = mc.run_id AND r.purpose = 'eval');

-- name: ListModelCallsForRun :many
SELECT * FROM model_calls WHERE run_id = $1 ORDER BY created_at;

-- name: ModelUsageSince :many
SELECT agent_name, alias, provider, model,
       count(*)::bigint AS calls,
       count(*) FILTER (WHERE status <> 'ok')::bigint AS errors,
       coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
       coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
       coalesce(sum(cost_usd), 0)::float8 AS cost_usd
FROM model_calls
WHERE created_at >= $1
GROUP BY agent_name, alias, provider, model
ORDER BY cost_usd DESC, calls DESC;

-- name: GetModelCache :one
SELECT response FROM model_cache WHERE key = $1 AND expires_at > now();

-- name: PutModelCache :exec
INSERT INTO model_cache (key, response, expires_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET response = EXCLUDED.response, expires_at = EXCLUDED.expires_at;

-- name: DeleteExpiredModelCache :execrows
DELETE FROM model_cache WHERE expires_at <= now();
