-- +goose Up
-- Phase 4: memory. Developer- and project-scoped knowledge (rules, preferences,
-- decisions, facts, conventions, approved examples). Extracted memories start as
-- candidates and only influence agents once the user approves them.
CREATE TABLE memories (
    id              uuid PRIMARY KEY,
    scope           text NOT NULL CHECK (scope IN ('developer', 'project')),
    project_id      uuid REFERENCES projects (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('rule', 'preference', 'decision', 'fact', 'convention', 'example')),
    category        text NOT NULL DEFAULT 'other'
                    CHECK (category IN ('coding', 'architecture', 'testing', 'api', 'database', 'security', 'tooling', 'product', 'other')),
    title           text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body            text NOT NULL CHECK (length(body) BETWEEN 1 AND 8000),
    status          text NOT NULL CHECK (status IN ('candidate', 'active', 'rejected', 'archived')),
    source          text NOT NULL CHECK (source IN ('manual', 'review_feedback', 'report', 'profile_inference', 'task')),
    source_refs     jsonb NOT NULL DEFAULT '{}',              -- taskId, approvalId, runId, artifactId…
    rationale       text NOT NULL DEFAULT '',
    duplicate_of    uuid REFERENCES memories (id) ON DELETE SET NULL,
    version         int NOT NULL DEFAULT 1,
    tsv             tsvector GENERATED ALWAYS AS (
                        setweight(to_tsvector('simple', title), 'A') || to_tsvector('simple', body)) STORED,
    embedding       vector,
    embedding_model text,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'project') = (project_id IS NOT NULL))
);
CREATE INDEX memories_lookup_idx ON memories (status, scope, project_id, kind);
CREATE INDEX memories_tsv_idx ON memories USING gin (tsv);
CREATE INDEX memories_title_trgm_idx ON memories USING gin (lower(title) gin_trgm_ops);

CREATE TABLE memory_versions (
    memory_id  uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    version    int NOT NULL,
    scope      text NOT NULL,
    project_id uuid,
    kind       text NOT NULL,
    category   text NOT NULL,
    title      text NOT NULL,
    body       text NOT NULL,
    status     text NOT NULL,
    changed_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (memory_id, version)
);

-- A memory-agent run and what it was extracting from; its output becomes candidates.
CREATE TABLE memory_extractions (
    run_id       uuid PRIMARY KEY REFERENCES agent_runs (id) ON DELETE CASCADE,
    source       text NOT NULL,
    source_refs  jsonb NOT NULL DEFAULT '{}',
    project_id   uuid REFERENCES projects (id) ON DELETE CASCADE,
    candidates   int NOT NULL DEFAULT 0,
    processed_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_extractions_pending_idx ON memory_extractions (created_at) WHERE processed_at IS NULL;

-- What an agent run was given (rules, relevant memories, project summary), frozen per run.
ALTER TABLE agent_runs ADD COLUMN context jsonb;

-- +goose Down
ALTER TABLE agent_runs DROP COLUMN context;
DROP TABLE memory_extractions;
DROP TABLE memory_versions;
DROP TABLE memories;
