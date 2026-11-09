-- name: QueueIndexJob :one
-- One pending/running job per repository: a second request returns no row.
INSERT INTO index_jobs (id, repository_id, reason)
VALUES ($1, $2, $3)
ON CONFLICT (repository_id) WHERE status IN ('queued', 'running') DO NOTHING
RETURNING *;

-- name: ClaimIndexJob :one
UPDATE index_jobs SET
    status = 'running', runtime_id = sqlc.arg(runtime_id), token_hash = sqlc.arg(token_hash),
    lease_expires_at = sqlc.arg(lease_expires_at), started_at = coalesce(started_at, now())
WHERE id = (
    SELECT j.id FROM index_jobs j
    WHERE j.status = 'queued' OR (j.status = 'running' AND j.lease_expires_at < now())
    ORDER BY j.created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *;

-- name: GetRunningIndexJobByToken :one
SELECT * FROM index_jobs WHERE token_hash = $1 AND status = 'running';

-- name: ExtendIndexJobLease :exec
UPDATE index_jobs SET lease_expires_at = $2 WHERE id = $1 AND status = 'running';

-- name: FinishIndexJob :one
UPDATE index_jobs SET status = sqlc.arg(status), commit_sha = sqlc.narg(commit_sha), stats = sqlc.arg(stats),
    error = sqlc.narg(error), finished_at = now(), token_hash = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetIndexJob :one
SELECT * FROM index_jobs WHERE id = $1;

-- name: ListIndexJobs :many
SELECT * FROM index_jobs WHERE repository_id = $1 ORDER BY created_at DESC LIMIT sqlc.arg(row_limit);

-- name: SetRepositoryIndexing :exec
UPDATE repositories SET index_status = sqlc.arg(status), index_error = sqlc.narg(error), updated_at = now() WHERE id = sqlc.arg(id);

-- name: SetRepositoryIndexed :exec
UPDATE repositories SET index_status = 'indexed', indexed_sha = sqlc.narg(indexed_sha), indexed_at = now(),
    index_stats = sqlc.arg(index_stats), index_error = NULL, updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ListCodeFileManifest :many
SELECT path, content_hash FROM code_files WHERE repository_id = $1;

-- name: UpsertCodeFile :one
INSERT INTO code_files (id, repository_id, path, language, size_bytes, content_hash, line_count, imports, skipped_reason, indexed_sha, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, sqlc.narg(skipped_reason), sqlc.narg(indexed_sha), now())
ON CONFLICT (repository_id, path) DO UPDATE SET
    language = EXCLUDED.language, size_bytes = EXCLUDED.size_bytes, content_hash = EXCLUDED.content_hash,
    line_count = EXCLUDED.line_count, imports = EXCLUDED.imports, skipped_reason = EXCLUDED.skipped_reason,
    indexed_sha = EXCLUDED.indexed_sha, updated_at = now()
RETURNING id;

-- name: DeleteFileSymbols :exec
DELETE FROM code_symbols WHERE file_id = $1;

-- name: DeleteFileChunks :exec
DELETE FROM code_chunks WHERE file_id = $1;

-- name: InsertCodeSymbols :copyfrom
INSERT INTO code_symbols (id, file_id, repository_id, name, kind, signature, parent, exported, start_line, end_line, doc)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: InsertCodeChunks :copyfrom
INSERT INTO code_chunks (id, file_id, repository_id, kind, start_line, end_line, heading, content, search_heading, search_text, content_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: DeleteCodeFiles :execrows
DELETE FROM code_files WHERE repository_id = $1 AND path = ANY (sqlc.arg(paths)::text[]);

-- name: DeleteRepoDependencies :exec
DELETE FROM repo_dependencies WHERE repository_id = $1;

-- name: InsertRepoDependencies :copyfrom
INSERT INTO repo_dependencies (id, repository_id, ecosystem, name, version, dev, manifest)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: CountIndex :one
SELECT
    (SELECT count(*) FROM code_files f WHERE f.repository_id = $1 AND f.skipped_reason IS NULL) AS files,
    (SELECT count(*) FROM code_files f WHERE f.repository_id = $1 AND f.skipped_reason IS NOT NULL) AS skipped,
    (SELECT count(*) FROM code_symbols s WHERE s.repository_id = $1) AS symbols,
    (SELECT count(*) FROM code_chunks c WHERE c.repository_id = $1) AS chunks,
    (SELECT count(*) FROM repo_dependencies d WHERE d.repository_id = $1) AS dependencies;

-- name: SearchChunks :many
SELECT c.id, c.kind, c.start_line, c.end_line, c.heading, c.content,
       f.path, f.language, r.id AS repository_id, r.name AS repository_name,
       r.indexed_sha, r.head_sha,
       -- Definitions (named chunks) rank above loose code; tests and generated-looking paths below sources.
       (ts_rank_cd(c.tsv, q.query)
         * CASE WHEN c.heading <> '' THEN 1.5 ELSE 1 END
         * CASE WHEN f.path ~* '(^|/)(tests?|__tests__|testdata|fixtures?|spec)/|[._-](test|spec)\.[a-z]+$' THEN 0.35 ELSE 1 END
       )::float8 AS rank
FROM code_chunks c
JOIN code_files f ON f.id = c.file_id
JOIN repositories r ON r.id = c.repository_id
CROSS JOIN (SELECT websearch_to_tsquery('simple', sqlc.arg(query)::text) AS query) q
WHERE r.project_id = sqlc.arg(project_id)
  AND c.tsv @@ q.query
  AND (sqlc.narg(repository_id)::uuid IS NULL OR r.id = sqlc.narg(repository_id))
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind))
ORDER BY rank DESC, f.path, c.start_line
LIMIT sqlc.arg(row_limit);

-- name: SearchChunksAny :many
-- Any-word fallback for natural-language queries (the query is a prepared to_tsquery OR expression).
SELECT c.id, c.kind, c.start_line, c.end_line, c.heading, c.content,
       f.path, f.language, r.id AS repository_id, r.name AS repository_name,
       r.indexed_sha, r.head_sha,
       -- Definitions (named chunks) rank above loose code; tests and generated-looking paths below sources.
       (ts_rank_cd(c.tsv, q.query)
         * CASE WHEN c.heading <> '' THEN 1.5 ELSE 1 END
         * CASE WHEN f.path ~* '(^|/)(tests?|__tests__|testdata|fixtures?|spec)/|[._-](test|spec)\.[a-z]+$' THEN 0.35 ELSE 1 END
       )::float8 AS rank
FROM code_chunks c
JOIN code_files f ON f.id = c.file_id
JOIN repositories r ON r.id = c.repository_id
CROSS JOIN (SELECT to_tsquery('simple', sqlc.arg(query)::text) AS query) q
WHERE r.project_id = sqlc.arg(project_id)
  AND c.tsv @@ q.query
  AND (sqlc.narg(repository_id)::uuid IS NULL OR r.id = sqlc.narg(repository_id))
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind))
ORDER BY rank DESC, f.path, c.start_line
LIMIT sqlc.arg(row_limit);

-- name: SearchSymbols :many
SELECT s.id, s.name, s.kind, s.signature, s.parent, s.exported, s.start_line, s.end_line, s.doc,
       f.path, f.language, r.id AS repository_id, r.name AS repository_name, r.indexed_sha, r.head_sha,
       similarity(lower(s.name), lower(sqlc.arg(query)::text))::float8 AS score
FROM code_symbols s
JOIN code_files f ON f.id = s.file_id
JOIN repositories r ON r.id = s.repository_id
WHERE r.project_id = sqlc.arg(project_id)
  AND (lower(s.name) = lower(sqlc.arg(query)::text)
       OR lower(s.name) LIKE lower(sqlc.arg(query)::text) || '%'
       OR lower(s.name) % lower(sqlc.arg(query)::text))
  AND (sqlc.narg(repository_id)::uuid IS NULL OR r.id = sqlc.narg(repository_id))
  AND (sqlc.narg(kind)::text IS NULL OR s.kind = sqlc.narg(kind))
ORDER BY (lower(s.name) = lower(sqlc.arg(query)::text)) DESC, s.exported DESC, score DESC, s.name
LIMIT sqlc.arg(row_limit);

-- name: ListProjectDependencies :many
SELECT d.ecosystem, d.name, d.version, d.dev, d.manifest, r.id AS repository_id, r.name AS repository_name
FROM repo_dependencies d JOIN repositories r ON r.id = d.repository_id
WHERE r.project_id = sqlc.arg(project_id)
  AND (sqlc.narg(repository_id)::uuid IS NULL OR r.id = sqlc.narg(repository_id))
ORDER BY r.name, d.dev, d.ecosystem, d.name;

-- name: ListReposNeedingIndex :many
-- Repositories whose HEAD moved since the last index and that have no pending job.
SELECT r.id FROM repositories r
WHERE r.source_type = 'local' AND r.head_sha IS NOT NULL
  AND r.head_sha IS DISTINCT FROM r.indexed_sha
  AND coalesce((r.settings->>'autoIndex')::bool, true)
  AND NOT EXISTS (SELECT 1 FROM index_jobs j WHERE j.repository_id = r.id AND j.status IN ('queued', 'running'))
  AND NOT EXISTS (SELECT 1 FROM index_jobs j WHERE j.repository_id = r.id AND j.status = 'failed'
                  AND j.finished_at > now() - interval '30 minutes');

-- name: ListLocalRepositories :many
SELECT * FROM repositories WHERE source_type = 'local' ORDER BY created_at;

-- name: GetCodeFileSkip :one
SELECT skipped_reason FROM code_files WHERE repository_id = $1 AND path = $2;

-- name: PendingEmbeddingChunks :many
-- Chunks without a vector from the current embedding model.
SELECT c.id, c.heading, c.content, c.content_hash, f.path
FROM code_chunks c JOIN code_files f ON f.id = c.file_id
WHERE c.embedding_model IS DISTINCT FROM sqlc.arg(model)::text
ORDER BY c.id
LIMIT sqlc.arg(row_limit);

-- name: SetChunkEmbedding :exec
-- Skipped when the chunk was replaced meanwhile (content hash changed).
UPDATE code_chunks SET embedding = sqlc.arg(vec)::text::vector, embedding_model = sqlc.arg(model), embedded_at = now()
WHERE id = sqlc.arg(id) AND content_hash = sqlc.arg(content_hash);

-- name: CountEmbeddings :one
SELECT count(*) FILTER (WHERE embedding_model = sqlc.arg(model)::text) AS embedded,
       count(*) FILTER (WHERE embedding_model IS DISTINCT FROM sqlc.arg(model)::text) AS pending
FROM code_chunks;

-- name: SemanticChunks :many
SELECT c.id, c.kind, c.start_line, c.end_line, c.heading, c.content,
       f.path, f.language, r.id AS repository_id, r.name AS repository_name,
       r.indexed_sha, r.head_sha,
       (c.embedding <=> sqlc.arg(vec)::text::vector)::float8 AS distance
FROM code_chunks c
JOIN code_files f ON f.id = c.file_id
JOIN repositories r ON r.id = c.repository_id
WHERE r.project_id = sqlc.arg(project_id)
  AND c.embedding_model = sqlc.arg(model)::text
  AND (sqlc.narg(repository_id)::uuid IS NULL OR r.id = sqlc.narg(repository_id))
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind))
ORDER BY distance
LIMIT sqlc.arg(row_limit);
