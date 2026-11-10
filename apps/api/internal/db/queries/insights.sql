-- name: InsightAgentStats :many
-- Per agent and definition version, over runs created since the window start.
SELECT r.agent_name, d.version AS definition_version, r.agent_definition_id,
       count(*)::bigint AS runs,
       count(*) FILTER (WHERE r.status = 'succeeded')::bigint AS succeeded,
       count(*) FILTER (WHERE r.status = 'failed')::bigint AS failed,
       count(*) FILTER (WHERE r.status = 'cancelled')::bigint AS cancelled,
       coalesce(sum(greatest(r.attempt - 1, 0)), 0)::bigint AS retries,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM r.finished_at - r.started_at)), 0)::float8 AS median_s,
       coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM r.finished_at - r.started_at)), 0)::float8 AS p90_s,
       coalesce(sum(r.total_cost_usd), 0)::float8 AS cost_usd,
       coalesce(sum(r.total_input_tokens + r.total_output_tokens), 0)::bigint AS tokens,
       (SELECT count(*) FROM agent_events e JOIN agent_runs r2 ON r2.id = e.run_id
        WHERE e.type = 'output.invalid' AND r2.agent_definition_id = r.agent_definition_id
          AND r2.created_at >= sqlc.arg(since) AND r2.purpose = 'work')::bigint AS invalid_outputs
FROM agent_runs r JOIN agent_definitions d ON d.id = r.agent_definition_id
WHERE r.created_at >= sqlc.arg(since) AND r.purpose = 'work'
GROUP BY r.agent_name, d.version, r.agent_definition_id
ORDER BY r.agent_name, d.version;

-- name: InsightFeedbackStats :many
SELECT agent_name, agent_definition_id,
       count(*) FILTER (WHERE rating = 'up')::bigint AS up,
       count(*) FILTER (WHERE rating = 'down')::bigint AS down
FROM feedback
WHERE updated_at >= sqlc.arg(since) AND agent_name IS NOT NULL
GROUP BY agent_name, agent_definition_id;

-- name: InsightDecidedApprovals :many
-- Decided approvals in the window with their workflow's plan (attribution is done in Go).
SELECT a.id, a.gate, a.status, a.comment, a.decided_at, w.id AS workflow_run_id, w.config, w.task_id
FROM approvals a JOIN workflow_runs w ON w.id = a.workflow_run_id
WHERE a.decided_at >= sqlc.arg(since) AND a.status IN ('approved', 'rejected')
ORDER BY a.decided_at DESC;

-- name: InsightRunsForWorkflows :many
-- The agent (and version) that ran each step of the given workflows (latest per stage).
SELECT DISTINCT ON (s.workflow_run_id, s.stage) s.workflow_run_id, s.stage, r.agent_name, r.agent_definition_id
FROM workflow_steps s JOIN agent_runs r ON r.id = s.agent_run_id
WHERE s.workflow_run_id = ANY(sqlc.arg(workflow_run_ids)::uuid[])
ORDER BY s.workflow_run_id, s.stage, s.started_at DESC;

-- name: InsightGateRounds :many
-- Failed quality-gate evaluations per workflow (each one is a fix round).
SELECT a.workflow_run_id, w.config, count(*)::bigint AS failed_rounds
FROM artifacts a JOIN workflow_runs w ON w.id = a.workflow_run_id
WHERE a.kind = 'quality_gate' AND a.created_at >= sqlc.arg(since) AND (a.content ->> 'passed')::boolean IS FALSE
GROUP BY a.workflow_run_id, w.config;

-- name: InsightProviderStats :many
SELECT mc.provider, mc.model,
       count(*)::bigint AS calls,
       count(*) FILTER (WHERE mc.status = 'error')::bigint AS errors,
       count(*) FILTER (WHERE mc.status = 'rejected')::bigint AS rejected,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY mc.latency_ms) FILTER (WHERE mc.status = 'ok'), 0)::float8 AS median_latency_ms,
       coalesce(sum(mc.cost_usd), 0)::float8 AS cost_usd,
       coalesce(sum(mc.input_tokens + mc.output_tokens), 0)::bigint AS tokens
FROM model_calls mc LEFT JOIN agent_runs r ON r.id = mc.run_id
WHERE mc.created_at >= sqlc.arg(since) AND (r.id IS NULL OR r.purpose = 'work')
GROUP BY mc.provider, mc.model
ORDER BY calls DESC;

-- name: InsightTemplateStats :many
WITH tw AS (
    SELECT x.id, coalesce(x.config ->> 'templateName', x.workflow_type)::text AS template, x.status, x.started_at, x.finished_at
    FROM workflow_runs x WHERE x.created_at >= sqlc.arg(since)
), cost AS (
    SELECT r.workflow_run_id, sum(r.total_cost_usd) AS cost
    FROM agent_runs r WHERE r.purpose = 'work' AND r.workflow_run_id IN (SELECT tw.id FROM tw)
    GROUP BY r.workflow_run_id
)
SELECT w.template::text AS template,
       count(*)::bigint AS runs,
       count(*) FILTER (WHERE w.status = 'completed')::bigint AS completed,
       count(*) FILTER (WHERE w.status = 'failed')::bigint AS failed,
       count(*) FILTER (WHERE w.status = 'cancelled')::bigint AS cancelled,
       count(*) FILTER (WHERE w.status = 'awaiting_retry')::bigint AS stalled,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM w.finished_at - w.started_at))
                FILTER (WHERE w.status = 'completed'), 0)::float8 AS median_s,
       coalesce(sum(cost.cost), 0)::float8 AS cost_usd
FROM tw w LEFT JOIN cost ON cost.workflow_run_id = w.id
GROUP BY w.template
ORDER BY runs DESC;

-- name: InsightStalls :many
-- Workflows waiting for a retry, by the step they failed at.
SELECT coalesce(config ->> 'templateName', workflow_type) AS template, coalesce(current_stage, '') AS stage, count(*)::bigint AS waiting
FROM workflow_runs WHERE status = 'awaiting_retry'
GROUP BY 1, 2 ORDER BY waiting DESC;

-- name: InsightFailures :many
-- Failed runs' errors in the window (clustered in Go).
SELECT r.id, r.agent_name, coalesce(r.error, '') AS error, r.created_at
FROM agent_runs r
WHERE r.created_at >= sqlc.arg(since) AND r.purpose = 'work' AND r.status = 'failed'
ORDER BY r.created_at DESC
LIMIT 1000;

-- name: InsightDailyRuns :many
SELECT date_trunc('day', r.created_at)::date AS day, count(*)::bigint AS runs,
       count(*) FILTER (WHERE r.status = 'succeeded')::bigint AS succeeded,
       coalesce(sum(r.total_cost_usd), 0)::float8 AS cost_usd
FROM agent_runs r
WHERE r.created_at >= sqlc.arg(since) AND r.purpose = 'work' AND r.agent_name = sqlc.arg(agent_name)
GROUP BY 1 ORDER BY 1;

-- name: InsightNegativeFeedback :many
SELECT f.id, f.target_type, f.target_id, f.run_id, f.note, f.updated_at
FROM feedback f
WHERE f.agent_name = sqlc.arg(agent_name) AND f.rating = 'down' AND f.updated_at >= sqlc.arg(since)
ORDER BY f.updated_at DESC LIMIT 50;
