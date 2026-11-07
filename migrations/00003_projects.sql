-- +goose Up
CREATE TABLE projects (
    id          uuid PRIMARY KEY,
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    metadata    jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);

CREATE TYPE project_resource_kind AS ENUM ('documentation', 'external_service', 'database_definition', 'other');

CREATE TABLE project_resources (
    id         uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind       project_resource_kind NOT NULL,
    name       text NOT NULL,
    uri        text NOT NULL DEFAULT '',
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_resources_project_id_idx ON project_resources (project_id);

CREATE TABLE project_services (
    id         uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text NOT NULL,
    kind       text NOT NULL DEFAULT '',
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- +goose Down
DROP TABLE project_services;
DROP TABLE project_resources;
DROP TYPE project_resource_kind;
DROP TABLE projects;
