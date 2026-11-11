-- name: ProjectActivity :many
-- One chronological feed of what happened in a project (newest first).
SELECT a.at::timestamptz AS at, a.kind::text AS kind, a.title::text AS title, a.detail::text AS detail,
       coalesce(a.task_id, '00000000-0000-0000-0000-000000000000')::uuid AS task_id, a.ref_id::uuid AS ref_id, a.url::text AS url
FROM (
    SELECT t.created_at AS at, 'task.created' AS kind, t.title, '' AS detail, t.id AS task_id, t.id AS ref_id, '' AS url
    FROM tasks t WHERE t.project_id = sqlc.arg(project_id)
  UNION ALL
    SELECT ap.created_at, 'approval.requested', t.title, ap.gate, t.id, ap.id, ''
    FROM approvals ap JOIN tasks t ON t.id = ap.task_id WHERE t.project_id = sqlc.arg(project_id)
  UNION ALL
    SELECT ap.decided_at, 'approval.' || ap.status, t.title, ap.gate || coalesce(': ' || ap.comment, ''), t.id, ap.id, ''
    FROM approvals ap JOIN tasks t ON t.id = ap.task_id
    WHERE t.project_id = sqlc.arg(project_id) AND ap.decided_at IS NOT NULL AND ap.status IN ('approved', 'rejected')
  UNION ALL
    SELECT w.started_at, 'workflow.started', t.title, coalesce(w.config ->> 'templateName', w.workflow_type), t.id, w.id, ''
    FROM workflow_runs w JOIN tasks t ON t.id = w.task_id WHERE t.project_id = sqlc.arg(project_id) AND w.started_at IS NOT NULL
  UNION ALL
    SELECT w.finished_at, 'workflow.' || w.status, t.title, coalesce(w.error, ''), t.id, w.id, ''
    FROM workflow_runs w JOIN tasks t ON t.id = w.task_id
    WHERE t.project_id = sqlc.arg(project_id) AND w.finished_at IS NOT NULL AND w.status IN ('completed', 'failed', 'cancelled')
  UNION ALL
    SELECT r.finished_at, 'run.failed', r.agent_name, left(coalesce(r.error, ''), 300), w.task_id, r.id, ''
    FROM agent_runs r LEFT JOIN workflow_runs w ON w.id = r.workflow_run_id
    WHERE r.project_id = sqlc.arg(project_id) AND r.status = 'failed' AND r.purpose = 'work' AND r.finished_at IS NOT NULL
  UNION ALL
    SELECT ar.created_at, 'branch.delivered', t.title, coalesce(ar.content ->> 'branch', 'no changes'), t.id, ar.id, ''
    FROM artifacts ar JOIN tasks t ON t.id = ar.task_id WHERE t.project_id = sqlc.arg(project_id) AND ar.kind = 'delivery'
  UNION ALL
    SELECT coalesce(p.pushed_at, p.updated_at), CASE WHEN p.status = 'pushed' THEN 'branch.pushed' ELSE 'push.failed' END, t.title,
           p.branch || ' → ' || p.remote || coalesce(' (' || p.error || ')', ''), t.id, p.id,
           CASE WHEN p.status = 'pushed' THEN regexp_replace(p.remote, '\.git$', '') || '/tree/' || p.branch ELSE '' END
    FROM git_pushes p JOIN tasks t ON t.id = p.task_id WHERE t.project_id = sqlc.arg(project_id) AND p.status <> 'granted'
  UNION ALL
    SELECT pr.created_at, 'pull_request.' || coalesce(pr.state, pr.status), t.title, coalesce('#' || pr.number::text || ' ', '') || pr.title,
           t.id, pr.id, coalesce(pr.url, '')
    FROM git_pull_requests pr JOIN git_pushes p ON p.id = pr.push_id JOIN tasks t ON t.id = p.task_id
    WHERE t.project_id = sqlc.arg(project_id) AND pr.status <> 'granted'
  UNION ALL
    SELECT m.created_at, 'memory.' || m.status, m.title, m.kind, NULL::uuid, m.id, ''
    FROM memories m WHERE m.project_id = sqlc.arg(project_id) AND m.status IN ('active', 'candidate')
) a
WHERE a.at < sqlc.arg(before)::timestamptz
  AND (cardinality(sqlc.arg(kinds)::text[]) = 0 OR split_part(a.kind, '.', 1) = ANY(sqlc.arg(kinds)::text[]))
ORDER BY a.at DESC
LIMIT sqlc.arg(row_limit);
