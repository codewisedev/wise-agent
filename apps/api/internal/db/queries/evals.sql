-- name: EvalCaseSources :many
-- Past work runs of an agent with what you thought of them: reviews that
-- pinned their output, and your ratings.
SELECT r.id AS run_id, r.input, r.project_id, a.status AS decision, coalesce(a.comment, '')::text AS note, 'approval'::text AS source
FROM approvals a
CROSS JOIN LATERAL jsonb_array_elements(a.artifacts) AS x
JOIN artifacts art ON art.id = (x ->> 'artifactId')::uuid
JOIN agent_runs r ON r.id = art.agent_run_id
WHERE r.agent_name = sqlc.arg(agent_name) AND r.purpose = 'work' AND r.status = 'succeeded'
  AND a.status IN ('approved', 'rejected')
UNION ALL
SELECT r.id, r.input, r.project_id, CASE WHEN f.rating = 'up' THEN 'approved' ELSE 'rejected' END, f.note, 'feedback'
FROM feedback f JOIN agent_runs r ON r.id = f.run_id
WHERE f.agent_name = sqlc.arg(agent_name) AND r.purpose = 'work' AND r.status = 'succeeded'
  AND (f.rating = 'up' OR f.note <> '');

-- name: InsertEvalCase :one
INSERT INTO eval_cases (id, agent_name, kind, input, project_id, source_run_id, source, expectation, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (source_run_id) WHERE source_run_id IS NOT NULL DO NOTHING
RETURNING *;

-- name: ListEvalCases :many
SELECT * FROM eval_cases WHERE agent_name = $1 ORDER BY created_at DESC LIMIT 200;

-- name: GetEvalCase :one
SELECT * FROM eval_cases WHERE id = $1;

-- name: UpdateEvalCase :one
UPDATE eval_cases SET enabled = $2, expectation = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteEvalCase :exec
DELETE FROM eval_cases WHERE id = $1;

-- name: EnabledEvalCases :many
-- Must-fix cases first (they show whether the change fixes what you rejected), then newest.
SELECT * FROM eval_cases WHERE agent_name = $1 AND enabled
ORDER BY CASE kind WHEN 'must_fix' THEN 0 WHEN 'custom' THEN 1 ELSE 2 END, created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: AgentAverageRunCost :one
SELECT coalesce(avg(total_cost_usd), 0)::float8 FROM agent_runs
WHERE agent_name = $1 AND purpose = 'work' AND status = 'succeeded' AND created_at >= now() - interval '30 days';

-- name: InsertEvalRun :one
INSERT INTO eval_runs (id, proposal_id, agent_name, baseline_definition_id, candidate_definition_id, baseline_model, candidate_model, judge_model, cost_cap_usd, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: InsertEvalResult :one
INSERT INTO eval_results (id, eval_run_id, case_id, case_kind, expectation, status) VALUES ($1, $2, $3, $4, $5, 'pending') RETURNING *;

-- name: ClaimPendingEvalResult :one
-- Starts the next case of an evaluation (one caller wins).
UPDATE eval_results SET status = 'running', updated_at = now()
WHERE id = (SELECT e.id FROM eval_results e WHERE e.eval_run_id = $1 AND e.status = 'pending' ORDER BY e.id LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: CountActiveEvalResults :one
SELECT count(*)::bigint FROM eval_results WHERE eval_run_id = $1 AND status IN ('running', 'judging');

-- name: ListRunningEvalRuns :many
SELECT id FROM eval_runs WHERE status = 'running';

-- name: SetEvalResultRuns :exec
UPDATE eval_results SET baseline_run_id = $2, candidate_run_id = $3, updated_at = now() WHERE id = $1;

-- name: GetEvalRun :one
SELECT * FROM eval_runs WHERE id = $1;

-- name: LockEvalRun :one
SELECT * FROM eval_runs WHERE id = $1 FOR UPDATE;

-- name: ListEvalRunsForProposal :many
SELECT * FROM eval_runs WHERE proposal_id = $1 ORDER BY created_at DESC LIMIT 20;

-- name: ListEvalResults :many
SELECT * FROM eval_results WHERE eval_run_id = $1 ORDER BY id;

-- name: EvalResultByRun :one
SELECT * FROM eval_results WHERE baseline_run_id = $1 OR candidate_run_id = $1 OR judge_run_id = $1 LIMIT 1;

-- name: LockEvalResult :one
SELECT * FROM eval_results WHERE id = $1 FOR UPDATE;

-- name: StartEvalJudging :exec
UPDATE eval_results SET status = 'judging', checks = $2, a_is = $3, updated_at = now() WHERE id = $1;

-- name: SetEvalJudgeRun :exec
UPDATE eval_results SET judge_run_id = $2, updated_at = now() WHERE id = $1;

-- name: FinishEvalResult :exec
UPDATE eval_results SET status = $2, checks = coalesce(sqlc.narg(checks), checks), baseline_score = $3, candidate_score = $4,
    reasoning = $5, error = $6, updated_at = now()
WHERE id = $1;

-- name: FinishEvalRun :exec
UPDATE eval_runs SET status = $2, summary = $3, error = $4, finished_at = now() WHERE id = $1;

-- name: EvalRunCost :one
SELECT coalesce(sum(r.total_cost_usd), 0)::float8 FROM agent_runs r
WHERE r.id IN (SELECT unnest(ARRAY[e.baseline_run_id, e.candidate_run_id, e.judge_run_id]) FROM eval_results e WHERE e.eval_run_id = $1);

-- name: RunInvalidOutputs :one
SELECT count(*)::bigint FROM agent_events WHERE run_id = $1 AND type = 'output.invalid';

-- name: PendingEvalWork :many
-- Results whose hook was missed: both replays finished, or a judge that never started or already finished.
SELECT e.id, e.status, e.baseline_run_id, e.candidate_run_id, e.judge_run_id FROM eval_results e
JOIN eval_runs v ON v.id = e.eval_run_id AND v.status = 'running'
WHERE (e.status = 'running' AND NOT EXISTS (
         SELECT 1 FROM agent_runs r WHERE r.id IN (e.baseline_run_id, e.candidate_run_id) AND r.status IN ('queued', 'running')))
   OR (e.status = 'judging' AND e.judge_run_id IS NULL AND e.updated_at < now() - interval '1 minute')
   OR (e.status = 'judging' AND EXISTS (
         SELECT 1 FROM agent_runs r WHERE r.id = e.judge_run_id AND r.status IN ('succeeded', 'failed', 'cancelled')));

-- name: ActiveEvalRunIDs :many
SELECT id FROM agent_runs
WHERE status IN ('queued', 'running') AND id IN (
    SELECT unnest(ARRAY[e.baseline_run_id, e.candidate_run_id, e.judge_run_id]) FROM eval_results e WHERE e.eval_run_id = $1);

-- name: GetEvalResult :one
SELECT * FROM eval_results WHERE id = $1;
