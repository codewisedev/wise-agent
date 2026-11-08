-- +goose Up
-- Phase 3c: agents are edited and created in the panel. Every save is a new row
-- (version history); code-shipped definitions are kept as defaults to reset to.
ALTER TABLE agent_definitions
    ADD COLUMN source     text NOT NULL DEFAULT 'code' CHECK (source IN ('code', 'panel')),
    ADD COLUMN base_hash  text,              -- the shipped version a panel edit started from
    ADD COLUMN archived   boolean NOT NULL DEFAULT false,
    ADD COLUMN created_by uuid REFERENCES users (id) ON DELETE SET NULL;
CREATE INDEX agent_definitions_name_idx ON agent_definitions (name, created_at DESC);

-- +goose Down
DROP INDEX agent_definitions_name_idx;
ALTER TABLE agent_definitions DROP COLUMN created_by, DROP COLUMN archived, DROP COLUMN base_hash, DROP COLUMN source;
