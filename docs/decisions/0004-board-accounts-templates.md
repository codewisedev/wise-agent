# 0004 — Board, accounts and configurable workflows (Phase 3b)

Status: accepted · 2026-11-08

## Context

Phase 3 ran one fixed pipeline. The user wanted: tasks on a Kanban board, fed by
hand or from Jira/ClickUp/Trello; several accounts per model provider (Claude
Code, Codex, LM Studio, DeepSeek…) chosen per workflow step; running a workflow
step by step and passing each result on; choosing which agents handle each task;
and a flowchart to see and edit it all.

## Decisions

**Accounts are stored encrypted in Postgres.** AES-256-GCM with
`WISE_SECRETS_KEY`, the row id as additional data. Secrets are write-only through
the API (a 4-character hint is shown), never logged, never sent to the agent
runtime. Changing an account's base URL requires re-entering its secret, so a
stored key cannot be redirected to another host. "Cloud" is derived from the
endpoint (only CLI providers and non-local URLs are cloud) and can only be
tightened, so the cloud allow-list cannot be bypassed by relabeling an account.

**CLI providers run in `wise-cli-runner`.** Claude Code and Codex ship their own
tools; Codex in particular cannot be reliably stripped of all of them across
versions. The runner container has no database access, no repositories and no
control-plane secrets — only internet egress and a shared runner token. The API
sends one call (prompt, schema, one account credential) per request. Codex
refreshes its OAuth tokens; the refreshed `auth.json` comes back and is stored.

**Workflows are templates interpreted by one Temporal workflow.** A template is
an ordered list of agent steps; each step may pin an account and model, pause for
review (`review`), pin earlier outputs in that review (`reviewIncludes`) and say
where changes restart (`reviseFrom`). Agents declare `produces` and `consumes`,
so inputs are wired by artifact kind and plans are validated (missing inputs,
disabled agents, bad references, unknown accounts) before saving or running.
A task picks a template and may override steps (enable, account, model,
review). The resolved plan is frozen into the run; only a waiting step's account
and model can still change (read by the activity when the step runs). "Run step
by step" sets review on every step. The Phase 3 workflow type was retired and the
migration closes any run still active.

**Linear flows only.** No branching or parallel steps: the flowchart is a
left-to-right sequence with review gates, edited with insert/move/delete and an
inspector. This keeps the engine and the editor simple; branching can come later
as a new workflow type.

**The board is a projection of tasks.** Column and position live on the task;
workflows move cards (start → In progress, review → Review, done → the template's
destination) and people can drag them anywhere. Positions are fractional so a
move writes one row.

**Task sources are import-only and run in the API.** Jira (JQL), ClickUp (list)
and Trello (board/lists) importers fetch with read scopes, upsert by external id
under a per-integration lease, let the source win on title/description/labels/
priority, and move cards on a source status change unless a workflow owns the
card. Syncing runs in the API process, which already has egress; the
orchestrator stays offline. Imported text reaches agents as data; prompts say
never to follow instructions inside it.

## Consequences

- Live tests against Jira, ClickUp and Trello need real tokens; importers are
  covered by stub-server tests.
- Templates are global (not per project); per-project templates can be added.
- The CLI runner holds credentials only for the duration of a call; its token
  must stay secret (it lets a caller spend the credentials it passes in).
