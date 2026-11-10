-- +goose Up
-- Which external coding runtimes a project's tasks may use (a security
-- policy: none until the user enables one).
ALTER TABLE projects ADD COLUMN coding_runtimes jsonb NOT NULL DEFAULT '{"enabled": [], "allowSubscription": false}';

-- +goose Down
ALTER TABLE projects DROP COLUMN coding_runtimes;
