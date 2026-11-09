-- +goose Up
-- Phase 4: the code index. A Python indexer (no database access) walks each
-- repository read-only and uploads files, AST symbols, text chunks and
-- dependencies through the runtime API. Everything is tied to a commit SHA.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE index_jobs (
    id               uuid PRIMARY KEY,
    repository_id    uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    status           text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    reason           text NOT NULL DEFAULT 'manual',          -- manual | head_changed | settings
    commit_sha       text,                                    -- set when the indexer finishes
    runtime_id       text,
    token_hash       bytea,
    lease_expires_at timestamptz,
    stats            jsonb NOT NULL DEFAULT '{}',
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz
);
CREATE INDEX index_jobs_repo_idx ON index_jobs (repository_id, created_at DESC);
-- At most one pending/running job per repository.
CREATE UNIQUE INDEX index_jobs_active_idx ON index_jobs (repository_id) WHERE status IN ('queued', 'running');
CREATE UNIQUE INDEX index_jobs_token_idx ON index_jobs (token_hash) WHERE token_hash IS NOT NULL;

CREATE TABLE code_files (
    id             uuid PRIMARY KEY,
    repository_id  uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path           text NOT NULL,
    language       text NOT NULL DEFAULT '',
    size_bytes     int NOT NULL DEFAULT 0,
    content_hash   text NOT NULL,
    line_count     int NOT NULL DEFAULT 0,
    imports        text[] NOT NULL DEFAULT '{}',
    skipped_reason text,                                      -- secret | too_large | binary
    indexed_sha    text,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repository_id, path)
);

CREATE TABLE code_symbols (
    id            uuid PRIMARY KEY,
    file_id       uuid NOT NULL REFERENCES code_files (id) ON DELETE CASCADE,
    repository_id uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name          text NOT NULL,
    kind          text NOT NULL,
    signature     text NOT NULL DEFAULT '',
    parent        text,
    exported      boolean NOT NULL DEFAULT false,
    start_line    int NOT NULL,
    end_line      int NOT NULL,
    doc           text NOT NULL DEFAULT ''
);
CREATE INDEX code_symbols_file_idx ON code_symbols (file_id);
CREATE INDEX code_symbols_repo_name_idx ON code_symbols (repository_id, lower(name));
CREATE INDEX code_symbols_name_trgm_idx ON code_symbols USING gin (lower(name) gin_trgm_ops);

CREATE TABLE code_chunks (
    id              uuid PRIMARY KEY,
    file_id         uuid NOT NULL REFERENCES code_files (id) ON DELETE CASCADE,
    repository_id   uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('code', 'doc', 'config')),
    start_line      int NOT NULL,
    end_line        int NOT NULL,
    heading         text NOT NULL DEFAULT '',                 -- symbol or section title
    content         text NOT NULL,
    search_heading  text NOT NULL DEFAULT '',                 -- heading + path with identifiers split
    search_text     text NOT NULL,                            -- content with identifiers split (fooBar -> foo bar)
    tsv             tsvector GENERATED ALWAYS AS (
                        setweight(to_tsvector('simple', search_heading), 'A') || to_tsvector('simple', search_text)) STORED,
    content_hash    text NOT NULL,
    embedding       vector,
    embedding_model text,
    embedded_at     timestamptz
);
CREATE INDEX code_chunks_file_idx ON code_chunks (file_id);
CREATE INDEX code_chunks_tsv_idx ON code_chunks USING gin (tsv);
CREATE INDEX code_chunks_pending_idx ON code_chunks (repository_id) WHERE embedding IS NULL;

CREATE TABLE repo_dependencies (
    id            uuid PRIMARY KEY,
    repository_id uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ecosystem     text NOT NULL,                              -- npm | go | pypi | cargo
    name          text NOT NULL,
    version       text NOT NULL DEFAULT '',
    dev           boolean NOT NULL DEFAULT false,
    manifest      text NOT NULL,
    UNIQUE (repository_id, ecosystem, name, manifest)
);

ALTER TABLE repositories
    ADD COLUMN indexed_sha text,
    ADD COLUMN indexed_at  timestamptz,
    ADD COLUMN index_stats jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN index_error text;

-- +goose Down
ALTER TABLE repositories DROP COLUMN index_error, DROP COLUMN index_stats, DROP COLUMN indexed_at, DROP COLUMN indexed_sha;
DROP TABLE repo_dependencies;
DROP TABLE code_chunks;
DROP TABLE code_symbols;
DROP TABLE code_files;
DROP TABLE index_jobs;
