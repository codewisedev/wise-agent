-- ---------------------------------------------------------------- tasks

-- name: CreateTask :one
INSERT INTO tasks (id, project_id, title, description, created_by, column_id, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = $1;

-- name: LockTask :one
SELECT * FROM tasks WHERE id = $1 FOR UPDATE;

-- name: ListTasks :many
SELECT t.*, p.name AS project_name, p.slug AS project_slug
FROM tasks t JOIN projects p ON p.id = t.project_id
WHERE (sqlc.narg(project_id)::uuid IS NULL OR t.project_id = sqlc.narg(project_id))
  AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status))
ORDER BY t.created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: SetTaskStatus :exec
UPDATE tasks SET status = $2, updated_at = now() WHERE id = $1;

-- ---------------------------------------------------------------- workflow runs

-- name: CreateWorkflowRun :one
INSERT INTO workflow_runs (id, task_id, workflow_type, config, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = $1;

-- name: LockWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = $1 FOR UPDATE;

-- name: GetLatestWorkflowRunForTask :one
SELECT * FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: ListWorkflowRunsForTask :many
SELECT * FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC;

-- name: UpdateWorkflowRunState :one
UPDATE workflow_runs SET
    status        = sqlc.arg(status),
    current_stage = coalesce(sqlc.narg(current_stage), current_stage),
    error         = sqlc.narg(error),
    started_at    = CASE WHEN sqlc.arg(status) <> 'pending' THEN coalesce(started_at, now()) ELSE started_at END,
    finished_at   = CASE WHEN sqlc.arg(status) IN ('completed', 'failed', 'cancelled') THEN now() ELSE NULL END,
    updated_at    = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetWorkflowTemporalRunID :exec
UPDATE workflow_runs SET temporal_run_id = $2, updated_at = now() WHERE id = $1;

-- ---------------------------------------------------------------- steps

-- name: UpsertWorkflowStep :one
INSERT INTO workflow_steps (id, workflow_run_id, stage, attempt, status, agent_run_id, artifact_id, error, finished_at)
VALUES (sqlc.arg(id), sqlc.arg(workflow_run_id), sqlc.arg(stage), sqlc.arg(attempt), sqlc.arg(status),
        sqlc.narg(agent_run_id), sqlc.narg(artifact_id), sqlc.narg(error),
        CASE WHEN sqlc.arg(status) IN ('succeeded', 'failed', 'cancelled', 'skipped') THEN now() END)
ON CONFLICT (workflow_run_id, stage, attempt) DO UPDATE SET
    status       = excluded.status,
    agent_run_id = coalesce(excluded.agent_run_id, workflow_steps.agent_run_id),
    artifact_id  = coalesce(excluded.artifact_id, workflow_steps.artifact_id),
    error        = excluded.error,
    finished_at  = excluded.finished_at
RETURNING *;

-- name: ListWorkflowSteps :many
SELECT * FROM workflow_steps WHERE workflow_run_id = $1 ORDER BY started_at, attempt;

-- name: CancelOpenWorkflowSteps :exec
UPDATE workflow_steps SET status = 'cancelled', finished_at = now()
WHERE workflow_run_id = $1 AND status IN ('running', 'awaiting_approval');

-- ---------------------------------------------------------------- artifacts

-- name: InsertArtifact :one
INSERT INTO artifacts (id, task_id, workflow_run_id, kind, version, content, content_hash, agent_run_id)
SELECT sqlc.arg(id), sqlc.arg(task_id), sqlc.arg(workflow_run_id), sqlc.arg(kind),
       coalesce(max(a.version), 0) + 1, sqlc.arg(content), sqlc.arg(content_hash), sqlc.narg(agent_run_id)
FROM artifacts a WHERE a.task_id = sqlc.arg(task_id) AND a.kind = sqlc.arg(kind)
RETURNING *;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = $1;

-- name: GetArtifactByAgentRun :one
SELECT * FROM artifacts WHERE agent_run_id = $1 ORDER BY version DESC LIMIT 1;

-- name: GetWorkflowArtifactOfKind :one
SELECT * FROM artifacts WHERE workflow_run_id = $1 AND kind = $2 ORDER BY version DESC LIMIT 1;

-- name: GetLatestArtifact :one
SELECT * FROM artifacts WHERE task_id = $1 AND kind = $2 ORDER BY version DESC LIMIT 1;

-- name: ListArtifacts :many
SELECT * FROM artifacts
WHERE task_id = sqlc.arg(task_id) AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
ORDER BY kind, version DESC;

-- ---------------------------------------------------------------- approvals

-- name: CreateApproval :one
INSERT INTO approvals (id, workflow_run_id, task_id, gate, round, artifacts, combined_hash, notes)
SELECT sqlc.arg(id), sqlc.arg(workflow_run_id), sqlc.arg(task_id), sqlc.arg(gate),
       coalesce(max(a.round), 0) + 1, sqlc.arg(artifacts), sqlc.arg(combined_hash), sqlc.arg(notes)
FROM approvals a WHERE a.workflow_run_id = sqlc.arg(workflow_run_id) AND a.gate = sqlc.arg(gate)
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = $1;

-- name: LockApproval :one
SELECT * FROM approvals WHERE id = $1 FOR UPDATE;

-- name: ListApprovals :many
SELECT sqlc.embed(a), t.title AS task_title, t.project_id, p.name AS project_name
FROM approvals a
JOIN tasks t ON t.id = a.task_id
JOIN projects p ON p.id = t.project_id
WHERE (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status))
  AND (sqlc.narg(task_id)::uuid IS NULL OR a.task_id = sqlc.narg(task_id))
ORDER BY a.created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: CountPendingApprovals :one
SELECT count(*) FROM approvals WHERE status = 'pending';

-- name: DecideApproval :one
UPDATE approvals SET status = $2, comment = $3, decided_by = $4, decided_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: ConsumeApproval :exec
UPDATE approvals SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL;

-- Approvals that reference a superseded artifact version become invalid.
-- name: SupersedeApprovalsForArtifactKind :many
UPDATE approvals SET status = 'superseded'
WHERE task_id = sqlc.arg(task_id)
  AND status IN ('pending', 'approved')
  AND consumed_at IS NULL
  AND artifacts @> jsonb_build_array(jsonb_build_object('kind', sqlc.arg(kind)::text))
RETURNING *;

-- name: CancelPendingApprovals :many
UPDATE approvals SET status = 'cancelled'
WHERE workflow_run_id = $1 AND status = 'pending'
RETURNING *;

-- ---------------------------------------------------------------- events

-- name: InsertWorkflowEvent :one
INSERT INTO workflow_events (workflow_run_id, seq, type, payload)
SELECT sqlc.arg(workflow_run_id), coalesce(max(e.seq), 0) + 1, sqlc.arg(type), sqlc.arg(payload)
FROM workflow_events e WHERE e.workflow_run_id = sqlc.arg(workflow_run_id)
RETURNING *;

-- name: GetWorkflowEventByID :one
SELECT * FROM workflow_events WHERE id = $1;

-- name: ListWorkflowEvents :many
SELECT * FROM workflow_events WHERE workflow_run_id = $1 AND seq > $2 ORDER BY seq LIMIT 1000;

-- name: NotifyWorkflowEvent :exec
SELECT pg_notify('wise_workflow_events', sqlc.arg(event_id)::text);

-- ---------------------------------------------------------------- outbox

-- name: InsertWorkflowCommand :exec
INSERT INTO workflow_commands (id, workflow_run_id, kind, payload) VALUES ($1, $2, $3, $4);

-- name: NotifyWorkflowCommand :exec
SELECT pg_notify('wise_workflow_commands', '');

-- name: ClaimWorkflowCommands :many
SELECT * FROM workflow_commands
WHERE processed_at IS NULL AND available_at <= now()
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(row_limit);

-- name: MarkWorkflowCommandProcessed :exec
UPDATE workflow_commands SET processed_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1;

-- name: MarkWorkflowCommandFailed :exec
UPDATE workflow_commands SET
    attempts     = attempts + 1,
    last_error   = sqlc.arg(last_error),
    available_at = now() + make_interval(secs => sqlc.arg(backoff_s)::int)
WHERE id = sqlc.arg(id);

-- ---------------------------------------------------------------- agent runs for workflows

-- name: GetAgentRunByIdempotencyKey :one
SELECT * FROM agent_runs WHERE idempotency_key = $1;

-- name: CreateWorkflowAgentRun :one
INSERT INTO agent_runs (id, agent_definition_id, agent_name, project_id, input, max_attempts, created_by,
                        workflow_run_id, idempotency_key, model_override)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
RETURNING *;

-- name: ListActiveWorkflowAgentRuns :many
SELECT id FROM agent_runs WHERE workflow_run_id = $1 AND status IN ('queued', 'running');

-- ---------------------------------------------------------------- board

-- name: ListBoardTasks :many
SELECT t.*,
       coalesce(w.id, '00000000-0000-0000-0000-000000000000'::uuid) AS workflow_id, -- nil UUID: never ran
       coalesce(w.status, '')::text AS workflow_status,
       w.current_stage AS workflow_stage,
       coalesce(w.config->>'templateName', '')::text AS template_name,
       EXISTS (SELECT 1 FROM approvals a WHERE a.workflow_run_id = w.id AND a.status = 'pending') AS pending_approval
FROM tasks t
LEFT JOIN LATERAL (
    SELECT * FROM workflow_runs r WHERE r.task_id = t.id ORDER BY r.created_at DESC LIMIT 1
) w ON true
WHERE t.project_id = $1
ORDER BY t.position, t.created_at;

-- name: NextBoardPosition :one
SELECT (coalesce(max(position), 0) + 1)::double precision FROM tasks WHERE column_id = $1;

-- name: UpdateTaskCard :one
UPDATE tasks SET
    title        = coalesce(sqlc.narg(title), title),
    description  = coalesce(sqlc.narg(description), description),
    priority     = coalesce(sqlc.narg(priority), priority),
    labels       = coalesce(sqlc.narg(labels)::text[], labels),
    column_id    = coalesce(sqlc.narg(column_id), column_id),
    position     = coalesce(sqlc.narg(position), position),
    updated_at   = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: MoveTaskCard :exec
UPDATE tasks SET column_id = $2, position = $3, updated_at = now() WHERE id = $1;
