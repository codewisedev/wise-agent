-- +goose Up
-- Phase 3b: multiple credentials per provider (model providers and task sources).
-- Secrets are AES-256-GCM encrypted by the API (internal/secrets) with the row id as
-- additional data; they are never returned by the API.
CREATE TABLE accounts (
    id                uuid PRIMARY KEY,
    name              text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    kind              text NOT NULL
                      CHECK (kind IN ('claude_code', 'codex_cli', 'openai_compatible', 'jira', 'clickup', 'trello')),
    base_url          text NOT NULL DEFAULT '',
    default_model     text NOT NULL DEFAULT '',
    cloud             boolean NOT NULL DEFAULT true,
    enabled           boolean NOT NULL DEFAULT true,
    secret_ciphertext text,
    secret_hint       text NOT NULL DEFAULT '',
    metadata          jsonb NOT NULL DEFAULT '{}',
    created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- A workflow step may pin the account (and model) its agent run uses.
ALTER TABLE agent_runs ADD COLUMN model_override jsonb;

-- +goose Down
ALTER TABLE agent_runs DROP COLUMN model_override;
DROP TABLE accounts;
