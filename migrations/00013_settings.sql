-- +goose Up
-- Phase 3c: panel-managed settings. Files under configs/ only provide defaults;
-- once seeded, these rows are the source of truth and the dashboard edits them.
CREATE TABLE app_settings (
    key        text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{1,62}$'),
    value      jsonb NOT NULL,
    version    int NOT NULL DEFAULT 1,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_settings_history (
    id         bigserial PRIMARY KEY,
    key        text NOT NULL REFERENCES app_settings (key) ON DELETE CASCADE,
    version    int NOT NULL,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (key, version)
);

-- +goose Down
DROP TABLE app_settings_history;
DROP TABLE app_settings;
