-- name: InsertMemory :one
INSERT INTO memories (id, scope, project_id, kind, category, title, body, status, source, source_refs, rationale, duplicate_of, created_by)
VALUES ($1, $2, sqlc.narg(project_id), $3, $4, $5, $6, $7, $8, $9, $10, sqlc.narg(duplicate_of), sqlc.narg(created_by))
RETURNING *;

-- name: GetMemory :one
SELECT * FROM memories WHERE id = $1;

-- name: LockMemory :one
SELECT * FROM memories WHERE id = $1 FOR UPDATE;

-- name: UpdateMemory :one
UPDATE memories SET scope = $2, project_id = sqlc.narg(project_id), kind = $3, category = $4, title = $5, body = $6,
    status = $7, version = version + 1, duplicate_of = sqlc.narg(duplicate_of),
    decided_by = coalesce(sqlc.narg(decided_by), decided_by), decided_at = coalesce(sqlc.narg(decided_at), decided_at),
    embedding = NULL, embedding_model = NULL, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: InsertMemoryVersion :exec
INSERT INTO memory_versions (memory_id, version, scope, project_id, kind, category, title, body, status, changed_by)
VALUES ($1, $2, $3, sqlc.narg(project_id), $4, $5, $6, $7, $8, sqlc.narg(changed_by))
ON CONFLICT (memory_id, version) DO NOTHING;

-- name: ListMemoryVersions :many
SELECT * FROM memory_versions WHERE memory_id = $1 ORDER BY version DESC;

-- name: GetMemoryVersion :one
SELECT * FROM memory_versions WHERE memory_id = $1 AND version = $2;

-- name: ListMemories :many
SELECT m.*, p.name AS project_name FROM memories m LEFT JOIN projects p ON p.id = m.project_id
WHERE (sqlc.narg(status)::text IS NULL OR m.status = sqlc.narg(status))
  AND (sqlc.narg(scope)::text IS NULL OR m.scope = sqlc.narg(scope))
  AND (sqlc.narg(project_id)::uuid IS NULL OR m.project_id = sqlc.narg(project_id))
  AND (sqlc.narg(kind)::text IS NULL OR m.kind = sqlc.narg(kind))
  AND (sqlc.narg(category)::text IS NULL OR m.category = sqlc.narg(category))
  AND (sqlc.narg(q)::text IS NULL OR m.tsv @@ websearch_to_tsquery('simple', sqlc.narg(q)) OR lower(m.title) LIKE '%' || lower(sqlc.narg(q)) || '%')
ORDER BY m.status = 'candidate' DESC, m.updated_at DESC
LIMIT sqlc.arg(row_limit);

-- name: CountMemoryCandidates :one
SELECT count(*) FROM memories WHERE status = 'candidate';

-- name: SimilarMemoriesByText :many
-- Same scope (and project), not rejected: candidates for "duplicate of".
SELECT id, title, body, kind, status, similarity(lower(title || ' ' || body), lower(sqlc.arg(text)::text))::float8 AS score
FROM memories
WHERE status IN ('candidate', 'active') AND scope = sqlc.arg(scope)
  AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)
  AND similarity(lower(title || ' ' || body), lower(sqlc.arg(text)::text)) > 0.2
ORDER BY score DESC
LIMIT 5;

-- name: SimilarMemoriesByVector :many
SELECT id, title, body, kind, status, (1 - (embedding <=> sqlc.arg(vec)::text::vector))::float8 AS score
FROM memories
WHERE status IN ('candidate', 'active') AND scope = sqlc.arg(scope)
  AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)
  AND embedding_model = sqlc.arg(model)::text
ORDER BY embedding <=> sqlc.arg(vec)::text::vector
LIMIT 5;

-- name: SetMemoryEmbedding :exec
UPDATE memories SET embedding = sqlc.arg(vec)::text::vector, embedding_model = sqlc.arg(model) WHERE id = sqlc.arg(id) AND version = sqlc.arg(version);

-- name: PendingMemoryEmbeddings :many
SELECT id, version, title, body FROM memories
WHERE status IN ('candidate', 'active') AND embedding_model IS DISTINCT FROM sqlc.arg(model)::text
ORDER BY updated_at LIMIT sqlc.arg(row_limit);

-- name: ActiveRules :many
-- Rules always apply: the developer's and this project's.
SELECT * FROM memories
WHERE status = 'active' AND kind = 'rule'
  AND (scope = 'developer' OR project_id = sqlc.narg(project_id))
ORDER BY scope, category, updated_at DESC
LIMIT 60;

-- name: RelevantMemoriesByVector :many
SELECT m.*, (1 - (m.embedding <=> sqlc.arg(vec)::text::vector))::float8 AS score FROM memories m
WHERE m.status = 'active' AND m.kind <> 'rule'
  AND (m.scope = 'developer' OR m.project_id = sqlc.narg(project_id))
  AND m.embedding_model = sqlc.arg(model)::text
ORDER BY m.embedding <=> sqlc.arg(vec)::text::vector
LIMIT sqlc.arg(row_limit);

-- name: RelevantMemoriesByText :many
SELECT m.*, ts_rank_cd(m.tsv, to_tsquery('simple', sqlc.arg(query)::text))::float8 AS score FROM memories m
WHERE m.status = 'active' AND m.kind <> 'rule'
  AND (m.scope = 'developer' OR m.project_id = sqlc.narg(project_id))
  AND m.tsv @@ to_tsquery('simple', sqlc.arg(query)::text)
ORDER BY score DESC
LIMIT sqlc.arg(row_limit);

-- name: InsertMemoryExtraction :exec
INSERT INTO memory_extractions (run_id, source, source_refs, project_id) VALUES ($1, $2, $3, sqlc.narg(project_id));

-- name: LockMemoryExtraction :one
SELECT * FROM memory_extractions WHERE run_id = $1 AND processed_at IS NULL FOR UPDATE SKIP LOCKED;

-- name: FinishMemoryExtraction :exec
UPDATE memory_extractions SET processed_at = now(), candidates = $2 WHERE run_id = $1;

-- name: ListFinishedUnprocessedExtractions :many
-- Extractions whose run ended but whose output was not turned into candidates (e.g. API restart).
SELECT e.run_id FROM memory_extractions e JOIN agent_runs r ON r.id = e.run_id
WHERE e.processed_at IS NULL AND r.status IN ('succeeded', 'failed', 'cancelled')
LIMIT 50;

-- name: SetAgentRunContext :exec
UPDATE agent_runs SET context = $2 WHERE id = $1 AND context IS NULL;

-- name: ListCompletedWorkflowsToLearnFrom :many
-- Recently completed workflows with a final report and no memory extraction yet.
SELECT w.id, w.task_id FROM workflow_runs w
WHERE w.status = 'completed' AND w.finished_at > now() - interval '2 days'
  AND EXISTS (SELECT 1 FROM artifacts a WHERE a.task_id = w.task_id AND a.kind = 'final_report')
  AND NOT EXISTS (SELECT 1 FROM agent_runs r WHERE r.idempotency_key = 'memory:workflow:' || w.id::text)
ORDER BY w.finished_at
LIMIT 10;
