-- +goose Up
-- Anthropic API-key accounts: used by Claude Code runners through the Runtime Proxy.
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check
    CHECK (kind IN ('claude_code', 'codex_cli', 'openai_compatible', 'anthropic', 'jira', 'clickup', 'trello'));

-- +goose Down
DELETE FROM accounts WHERE kind = 'anthropic';
ALTER TABLE accounts DROP CONSTRAINT accounts_kind_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_kind_check
    CHECK (kind IN ('claude_code', 'codex_cli', 'openai_compatible', 'jira', 'clickup', 'trello'));
