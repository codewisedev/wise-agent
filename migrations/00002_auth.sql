-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      citext NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'member')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    disabled_at   timestamptz
);

-- id is the SHA-256 of the opaque session token; the raw token is never stored.
CREATE TABLE sessions (
    id           bytea PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    user_agent   text NOT NULL DEFAULT '',
    ip           text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL UNIQUE,
    token_hash   bytea NOT NULL,
    scopes       text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
