-- name: CostBreakdown :many
-- Spend per day and dimension. billing: subscription (CLI subscriptions report
-- API-equivalent cost), api (pay per use) or local (no cost).
SELECT date_trunc('day', mc.created_at)::date AS day,
       coalesce(p.name, '(no project)')::text AS project,
       mc.agent_name::text AS agent,
       (mc.provider || '/' || mc.model)::text AS model,
       (CASE WHEN mc.alias LIKE 'coding:%' THEN split_part(mc.alias, ':', 2) ELSE 'native' END)::text AS runtime,
       (CASE
          WHEN mc.alias LIKE 'coding:%:subscription' THEN 'subscription'
          WHEN mc.alias LIKE 'coding:%' THEN 'api'
          WHEN mc.provider IN ('claude-code', 'codex') OR mc.provider IN (SELECT a.name FROM accounts a WHERE a.kind IN ('claude_code', 'codex_cli')) THEN 'subscription'
          WHEN mc.cost_usd = 0 THEN 'local'
          ELSE 'api' END)::text AS billing,
       coalesce(r.purpose, 'work')::text AS purpose,
       coalesce(sum(mc.cost_usd), 0)::float8 AS cost_usd,
       coalesce(sum(mc.input_tokens + mc.output_tokens), 0)::bigint AS tokens,
       count(*)::bigint AS calls
FROM model_calls mc
LEFT JOIN agent_runs r ON r.id = mc.run_id
LEFT JOIN projects p ON p.id = r.project_id
WHERE mc.created_at >= sqlc.arg(since) AND mc.status = 'ok'
GROUP BY 1, 2, 3, 4, 5, 6, 7
ORDER BY 1;

-- name: CostTopTasks :many
SELECT t.id, t.title, coalesce(sum(r.total_cost_usd), 0)::float8 AS cost_usd, count(r.id)::bigint AS runs
FROM agent_runs r JOIN workflow_runs w ON w.id = r.workflow_run_id JOIN tasks t ON t.id = w.task_id
WHERE r.created_at >= sqlc.arg(since) AND r.purpose = 'work'
GROUP BY t.id, t.title
HAVING sum(r.total_cost_usd) > 0
ORDER BY cost_usd DESC
LIMIT 10;

-- name: AgentSpendToday :many
-- Spend counted against daily budgets (real work only, as the gateway counts it).
SELECT mc.agent_name, coalesce(sum(mc.cost_usd), 0)::float8 AS cost_usd
FROM model_calls mc
WHERE mc.created_at >= date_trunc('day', now()) AND mc.status = 'ok'
  AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.id = mc.run_id AND r.purpose = 'eval')
GROUP BY mc.agent_name
ORDER BY 2 DESC;
