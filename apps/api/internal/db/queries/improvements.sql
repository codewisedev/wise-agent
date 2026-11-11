-- name: InsertImprovementAnalysis :one
INSERT INTO improvement_analyses (id, trigger, period, window_days, status, digest, created_by, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg(finished_at))
RETURNING *;

-- name: SetImprovementAnalysisRun :exec
UPDATE improvement_analyses SET agent_run_id = $2 WHERE id = $1;

-- name: GetImprovementAnalysisByRun :one
SELECT * FROM improvement_analyses WHERE agent_run_id = $1;

-- name: LockImprovementAnalysis :one
SELECT * FROM improvement_analyses WHERE id = $1 AND status = 'running' FOR UPDATE;

-- name: FinishImprovementAnalysis :exec
UPDATE improvement_analyses SET status = $2, proposals = $3, refused = $4, error = $5, finished_at = now() WHERE id = $1;

-- name: ListImprovementAnalyses :many
SELECT * FROM improvement_analyses ORDER BY created_at DESC LIMIT $1;

-- name: GetImprovementAnalysis :one
SELECT * FROM improvement_analyses WHERE id = $1;

-- name: ListUnprocessedImprovementAnalyses :many
-- Running analyses whose agent run has finished (the hook was missed).
SELECT a.id, a.agent_run_id FROM improvement_analyses a JOIN agent_runs r ON r.id = a.agent_run_id
WHERE a.status = 'running' AND r.status IN ('succeeded', 'failed', 'cancelled');

-- name: FailStaleImprovementAnalyses :exec
-- Analyses whose run never started (e.g. enqueue failed) are closed after an hour.
UPDATE improvement_analyses SET status = 'failed', error = 'the analysis run did not start', finished_at = now()
WHERE status = 'running' AND agent_run_id IS NULL AND created_at < now() - interval '1 hour';

-- name: InsertImprovementProposal :one
INSERT INTO improvement_proposals (id, analysis_id, kind, target, title, rationale, evidence, change, before, base, fingerprint, metrics_before)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: ListImprovementProposals :many
SELECT * FROM improvement_proposals
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.narg(target)::text IS NULL OR target = sqlc.narg(target))
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: GetImprovementProposal :one
SELECT * FROM improvement_proposals WHERE id = $1;

-- name: LockImprovementProposal :one
SELECT * FROM improvement_proposals WHERE id = $1 FOR UPDATE;

-- name: SetImprovementProposalStatus :one
UPDATE improvement_proposals
SET status = $2, decision_note = coalesce(sqlc.narg(note), decision_note), decided_by = coalesce(sqlc.narg(decided_by), decided_by),
    decided_at = CASE WHEN $2 IN ('rejected', 'applied') THEN now() ELSE decided_at END, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CountOpenImprovementProposals :one
SELECT count(*)::bigint FROM improvement_proposals WHERE status IN ('proposed', 'evaluating', 'evaluated');

-- name: ImprovementFingerprintStatus :one
-- The latest proposal with this fingerprint (open, or decided recently).
SELECT status FROM improvement_proposals WHERE fingerprint = $1 AND (status IN ('proposed', 'evaluating', 'evaluated') OR updated_at > now() - interval '90 days')
ORDER BY updated_at DESC LIMIT 1;

-- name: PastImprovementProposals :many
-- System memory fed back to the next analysis: what was proposed and what happened.
SELECT kind, target, title, status, decision_note, updated_at FROM improvement_proposals
WHERE status IN ('applied', 'rejected', 'rolled_back') ORDER BY updated_at DESC LIMIT 30;

-- name: EvidenceRunsExist :many
SELECT id FROM agent_runs WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND purpose = 'work';

-- name: EvidenceApprovalsExist :many
SELECT id FROM approvals WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: EvidenceFeedbackExist :many
SELECT id FROM feedback WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: EvidenceMemoriesExist :many
SELECT id FROM memories WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: MemoryRetrievalStats :many
-- How often each active memory was given to agents (from the context frozen on every run).
WITH used AS (
    SELECT (ref ->> 'id')::uuid AS id, r.created_at
    FROM agent_runs r,
         jsonb_array_elements(coalesce(r.context -> 'rules', '[]'::jsonb) || coalesce(r.context -> 'relevant', '[]'::jsonb)) AS ref
    WHERE r.context IS NOT NULL AND r.purpose = 'work' AND (ref ->> 'id') ~ '^[0-9a-f-]{36}$'
)
SELECT m.id, m.kind, m.scope, m.category, m.title, m.body, m.version, m.created_at,
       count(used.id)::bigint AS retrievals, coalesce(max(used.created_at), m.created_at)::timestamptz AS last_retrieved
FROM memories m LEFT JOIN used ON used.id = m.id
WHERE m.status = 'active'
GROUP BY m.id
ORDER BY m.created_at;

-- name: RecentNegativeFeedback :many
SELECT f.id, f.agent_name, f.run_id, f.note FROM feedback f
WHERE f.rating = 'down' AND f.note <> '' AND f.agent_name IS NOT NULL AND f.updated_at >= sqlc.arg(since)
ORDER BY f.updated_at DESC LIMIT 40;

-- name: SetImprovementProposalApplied :one
UPDATE improvement_proposals
SET status = 'applied', applied = $2, metrics_before = coalesce(sqlc.narg(metrics), metrics_before), decided_by = $3,
    decided_at = now(), applied_at = now(), updated_at = now()
WHERE id = $1 AND status = 'applying'
RETURNING *;

-- name: ClaimImprovementProposalForApply :one
-- Only one Apply can claim an open proposal.
UPDATE improvement_proposals SET status = 'applying', updated_at = now()
WHERE id = $1 AND status IN ('proposed', 'evaluated')
RETURNING *;

-- name: ReleaseImprovementProposal :exec
UPDATE improvement_proposals SET status = $2, updated_at = now() WHERE id = $1 AND status = 'applying';

-- name: ClaimImprovementProposalForRollback :one
-- Only one rollback can claim an applied proposal; released to 'applied' if the restore fails.
UPDATE improvement_proposals SET status = 'rolled_back', rolled_back_at = now(), updated_at = now()
WHERE id = $1 AND status = 'applied'
RETURNING *;

-- name: ReleaseImprovementRollback :exec
UPDATE improvement_proposals SET status = 'applied', rolled_back_at = NULL, updated_at = now() WHERE id = $1 AND status = 'rolled_back';

-- name: RecoverStaleApplying :many
-- Applies interrupted (crash, deploy) more than 15 minutes ago: the target may or may not have been
-- written, so the proposal becomes stale (it can only be rejected) and says where to look.
UPDATE improvement_proposals
SET status = 'stale', updated_at = now(),
    decision_note = 'An apply was interrupted; check the target''s history (agent versions, setting history, template history) before proposing again.'
WHERE status = 'applying' AND updated_at < now() - interval '15 minutes'
RETURNING id;

-- name: SetImprovementProposalRolledBack :one
UPDATE improvement_proposals SET decision_note = coalesce(sqlc.narg(note), decision_note), updated_at = now()
WHERE id = $1 AND status = 'rolled_back'
RETURNING *;

-- name: AppliedImprovementProposals :many
SELECT * FROM improvement_proposals WHERE status = 'applied' ORDER BY applied_at DESC LIMIT 10;
