-- +goose Up
-- Git hosting accounts (GitHub, GitLab) for pushes and pull requests, and
-- where each repository releases to.
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check
    CHECK (kind IN ('claude_code', 'codex_cli', 'openai_compatible', 'anthropic', 'jira', 'clickup', 'trello', 'github', 'gitlab'));

-- {remote: https URL, accountId, baseBranch}
ALTER TABLE repositories ADD COLUMN release jsonb NOT NULL DEFAULT '{}';
-- {pullRequests: off|ask|auto, draft: bool}
ALTER TABLE projects ADD COLUMN release_policy jsonb NOT NULL DEFAULT '{"pullRequests": "ask", "draft": true}';

-- +goose Down
ALTER TABLE projects DROP COLUMN release_policy;
ALTER TABLE repositories DROP COLUMN release;
DELETE FROM accounts WHERE kind IN ('github', 'gitlab');
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check
    CHECK (kind IN ('claude_code', 'codex_cli', 'openai_compatible', 'anthropic', 'jira', 'clickup', 'trello'));
