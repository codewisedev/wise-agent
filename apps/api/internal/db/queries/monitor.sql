-- name: MonitorActiveRuns :many
-- Queued and running agent runs with where they belong and their last event.
SELECT r.id, r.agent_name, r.status::text AS status, r.purpose, r.attempt, r.max_attempts, r.lease_expires_at, r.cancel_requested,
       r.created_at, r.started_at, (r.total_input_tokens + r.total_output_tokens)::bigint AS tokens, r.total_cost_usd,
       coalesce(r.model_override ->> 'runtime', '')::text AS runtime, coalesce(r.model_override ->> 'account', '')::text AS account,
       r.project_id, w.task_id, t.title AS task_title, p.name AS project_name,
       coalesce(ev.type, '')::text AS last_event, (ev.created_at IS NOT NULL)::boolean AS has_event,
       coalesce(ev.created_at, r.created_at)::timestamptz AS last_event_at
FROM agent_runs r
LEFT JOIN workflow_runs w ON w.id = r.workflow_run_id
LEFT JOIN tasks t ON t.id = w.task_id
LEFT JOIN projects p ON p.id = r.project_id
LEFT JOIN LATERAL (SELECT e.type, e.created_at FROM agent_events e WHERE e.run_id = r.id ORDER BY e.seq DESC LIMIT 1) ev ON true
WHERE r.status IN ('queued', 'running')
ORDER BY r.created_at
LIMIT 200;

-- name: MonitorWaitingWorkflows :many
-- Workflows waiting for you: an approval or a retry.
SELECT w.id, w.task_id, t.title AS task_title, p.name AS project_name, w.status, coalesce(w.current_stage, '')::text AS stage, w.error, w.updated_at
FROM workflow_runs w JOIN tasks t ON t.id = w.task_id JOIN projects p ON p.id = t.project_id
WHERE w.status IN ('awaiting_approval', 'awaiting_retry')
ORDER BY w.updated_at
LIMIT 200;
