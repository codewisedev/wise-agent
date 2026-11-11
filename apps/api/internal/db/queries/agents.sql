-- name: GetAgentDefinitionByHash :one
SELECT * FROM agent_definitions WHERE content_hash = $1;

-- name: ClearCurrentAgentDefinition :exec
UPDATE agent_definitions SET is_current = false WHERE name = $1 AND is_current AND id <> $2;

-- name: SetAgentDefinitionCurrent :exec
UPDATE agent_definitions SET is_current = true WHERE id = $1;

-- name: InsertAgentDefinition :one
INSERT INTO agent_definitions (id, name, version, content_hash, definition, prompt, enabled, is_current, source, base_hash, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, false, sqlc.arg(source), sqlc.narg(base_hash), sqlc.narg(created_by))
RETURNING *;

-- name: GetLatestShippedAgentDefinition :one
SELECT * FROM agent_definitions WHERE name = $1 AND source = 'code' ORDER BY created_at DESC LIMIT 1;

-- name: ListAgentDefinitionVersions :many
SELECT * FROM agent_definitions WHERE name = $1 AND NOT candidate ORDER BY created_at DESC LIMIT 100;

-- name: CountAgentDefinitionsByName :one
SELECT count(*) FROM agent_definitions WHERE name = $1 AND NOT archived;

-- name: ArchiveAgent :exec
UPDATE agent_definitions SET archived = true, is_current = false WHERE name = $1;

-- name: UnarchiveAgentDefinition :exec
UPDATE agent_definitions SET archived = false WHERE id = $1;

-- name: GetAgentDefinition :one
SELECT * FROM agent_definitions WHERE id = $1;

-- name: GetCurrentAgentDefinitionByName :one
SELECT * FROM agent_definitions WHERE name = $1 AND is_current;

-- name: ListCurrentAgentDefinitions :many
SELECT sqlc.embed(d), s.run_count, s.last_run_at
FROM agent_definitions d
LEFT JOIN LATERAL (
    SELECT count(*) AS run_count, max(r.created_at) AS last_run_at
    FROM agent_runs r WHERE r.agent_name = d.name
) s ON true
WHERE d.is_current
ORDER BY d.enabled DESC, d.name;

-- name: CreateAgentRun :one
INSERT INTO agent_runs (id, agent_definition_id, agent_name, project_id, input, max_attempts, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAgentRun :one
SELECT * FROM agent_runs WHERE id = $1;

-- name: LockAgentRun :one
SELECT * FROM agent_runs WHERE id = $1 FOR UPDATE;

-- name: ListAgentRuns :many
-- Real work only: evaluation replays are listed with their evaluation.
SELECT * FROM agent_runs
WHERE purpose = 'work'
  AND (sqlc.narg(agent_name)::text IS NULL OR agent_name = sqlc.narg(agent_name))
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id))
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: ClaimAgentRun :one
UPDATE agent_runs SET
    status           = 'running',
    attempt          = attempt + 1,
    runtime_id       = sqlc.arg(runtime_id),
    run_token_hash   = sqlc.arg(run_token_hash),
    lease_expires_at = sqlc.arg(lease_expires_at),
    started_at       = coalesce(started_at, now()),
    updated_at       = now()
WHERE id = (
    SELECT q.id FROM agent_runs q
    WHERE q.status = 'queued'
    ORDER BY q.created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: GetRunningRunByTokenHash :one
SELECT * FROM agent_runs WHERE run_token_hash = $1 AND status = 'running';

-- name: ExtendRunLease :one
UPDATE agent_runs SET lease_expires_at = $2, updated_at = now()
WHERE id = $1 AND status = 'running'
RETURNING cancel_requested;

-- name: FinishRun :one
UPDATE agent_runs SET
    status           = sqlc.arg(status),
    output           = sqlc.narg(output),
    error            = sqlc.narg(error),
    run_token_hash   = NULL,
    lease_expires_at = NULL,
    finished_at      = now(),
    updated_at       = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RequeueRun :one
UPDATE agent_runs SET
    status = 'queued', runtime_id = NULL, run_token_hash = NULL, lease_expires_at = NULL, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RequestRunCancel :one
UPDATE agent_runs SET cancel_requested = true, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListExpiredRunLeases :many
SELECT * FROM agent_runs
WHERE status = 'running' AND lease_expires_at < now()
ORDER BY lease_expires_at
FOR UPDATE SKIP LOCKED
LIMIT 50;

-- name: AddRunUsage :exec
UPDATE agent_runs SET
    total_input_tokens  = total_input_tokens + sqlc.arg(input_tokens),
    total_output_tokens = total_output_tokens + sqlc.arg(output_tokens),
    total_cost_usd      = total_cost_usd + sqlc.arg(cost_usd),
    updated_at          = now()
WHERE id = sqlc.arg(id);

-- name: InsertAgentEvent :one
INSERT INTO agent_events (run_id, seq, type, payload)
SELECT sqlc.arg(run_id), coalesce(max(e.seq), 0) + 1, sqlc.arg(type), sqlc.arg(payload)
FROM agent_events e WHERE e.run_id = sqlc.arg(run_id)
RETURNING *;

-- name: ListAgentEvents :many
SELECT * FROM agent_events WHERE run_id = $1 AND seq > $2 ORDER BY seq LIMIT 1000;

-- name: InsertToolCall :exec
INSERT INTO tool_calls (id, run_id, tool, input, output, status, error, duration_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertCandidateAgentDefinition :one
-- A version made only to be evaluated (never current until applied).
INSERT INTO agent_definitions (id, name, version, content_hash, definition, prompt, enabled, is_current, source, base_hash, created_by, candidate)
VALUES ($1, $2, $3, $4, $5, $6, $7, false, 'panel', sqlc.narg(base_hash), sqlc.narg(created_by), true)
RETURNING *;

-- name: ClearAgentDefinitionCandidate :exec
UPDATE agent_definitions SET candidate = false WHERE id = $1;
