-- The release agent was never used: commit messages come from the implementing
-- agent, pull request descriptions from the final report, and pushes go through
-- the git worker (ADR 0010). It is no longer shipped; archive the copy existing
-- installs registered, as deleting it in the panel would, unless the user edited
-- it in the panel or a workflow template uses it.

-- +goose Up
UPDATE agent_definitions SET archived = true, is_current = false
WHERE name = 'release-agent'
  AND NOT EXISTS (SELECT 1 FROM agent_definitions WHERE name = 'release-agent' AND source = 'panel')
  AND NOT EXISTS (SELECT 1 FROM workflow_templates WHERE definition::text LIKE '%"release-agent"%');

-- +goose Down
UPDATE agent_definitions SET archived = false
WHERE name = 'release-agent' AND source = 'code';
UPDATE agent_definitions SET is_current = true
WHERE id = (SELECT id FROM agent_definitions WHERE name = 'release-agent' AND source = 'code' ORDER BY created_at DESC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM agent_definitions WHERE name = 'release-agent' AND is_current);
