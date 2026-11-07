-- +goose Up
CREATE TYPE repository_role AS ENUM ('backend', 'frontend', 'mobile', 'shared_library', 'infrastructure', 'documentation', 'other');
CREATE TYPE repository_source_type AS ENUM ('local', 'remote');
CREATE TYPE repository_index_status AS ENUM ('not_indexed', 'queued', 'indexing', 'indexed', 'failed');
CREATE TYPE repository_relationship_kind AS ENUM ('depends_on', 'consumes_api', 'shares_library', 'deploys');

CREATE TABLE repositories (
    id               uuid PRIMARY KEY,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name             text NOT NULL,
    role             repository_role NOT NULL DEFAULT 'other',
    source_type      repository_source_type NOT NULL,
    local_path       text,
    remote_url       text,
    default_branch   text,
    current_branch   text,
    head_sha         text,
    git_remotes      jsonb NOT NULL DEFAULT '[]',
    git_inspected_at timestamptz,
    git_error        text,
    index_status     repository_index_status NOT NULL DEFAULT 'not_indexed',
    index_requested_at timestamptz,
    settings         jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name),
    CHECK (
        (source_type = 'local' AND local_path IS NOT NULL) OR
        (source_type = 'remote' AND remote_url IS NOT NULL)
    )
);
CREATE INDEX repositories_project_id_idx ON repositories (project_id);

CREATE TABLE repository_relationships (
    id                 uuid PRIMARY KEY,
    project_id         uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    from_repository_id uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    to_repository_id   uuid NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    kind               repository_relationship_kind NOT NULL,
    note               text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (from_repository_id, to_repository_id, kind),
    CHECK (from_repository_id <> to_repository_id)
);
CREATE INDEX repository_relationships_project_id_idx ON repository_relationships (project_id);

-- +goose Down
DROP TABLE repository_relationships;
DROP TABLE repositories;
DROP TYPE repository_relationship_kind;
DROP TYPE repository_index_status;
DROP TYPE repository_source_type;
DROP TYPE repository_role;
