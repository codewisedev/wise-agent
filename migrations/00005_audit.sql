-- +goose Up
CREATE TABLE audit_logs (
    id          uuid PRIMARY KEY,
    actor_type  text NOT NULL CHECK (actor_type IN ('user', 'api_token', 'system')),
    actor_id    uuid,
    action      text NOT NULL,
    entity_type text NOT NULL,
    entity_id   uuid,
    payload     jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_entity_idx ON audit_logs (entity_type, entity_id, created_at DESC);
CREATE INDEX audit_logs_created_at_idx ON audit_logs (created_at DESC);

-- +goose Down
DROP TABLE audit_logs;
