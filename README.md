# Wise

Personal, local-first AI engineering platform. See [`implementation-plan.md`](implementation-plan.md) for the full vision and roadmap.

**Status: Phase 3c — Everything configurable from the panel.** Wise manages projects and repositories behind a login, keeps tasks on Trello-style boards with your own lists (by hand or imported from Jira, ClickUp and Trello), and runs them through configurable agent workflows on Temporal — any step on any of your accounts (Claude Code, Codex, LM Studio, DeepSeek…), with review gates or fully step by step. Agents, prompts, models, policies and limits are all edited in the dashboard. Context indexing and the coding pipeline arrive in later phases.

## What's here

| Path | What |
| --- | --- |
| `apps/api` | Go control-plane API (chi, pgx, sqlc, goose, oapi-codegen) |
| `apps/dashboard` | React + TypeScript dashboard (Vite, React Router, TanStack Query) |
| `packages/contracts/api/openapi.yaml` | **Source of truth** for the REST API; Go server and TS client types are generated from it |
| `migrations/` | goose SQL migrations (embedded into the API binary) |
| `apps/agent-runtime` | Python agent runtime (WiseLoop, read-only repository tools) |
| `packages/agent-definitions`, `packages/prompts` | Agent contracts (YAML) and versioned system prompts |
| `packages/contracts/{agents,events}` | JSON Schemas for agent/tool definitions and agent events |
| `configs/models`, `configs/policies`, `configs/board` | Shipped *defaults* for models, usage policy and board lists (edited afterwards in Settings) |
| `apps/api/internal/{workflow,tasks}` | Temporal workflow, activities, outbox dispatcher; tasks, artifacts, approvals |
| `configs/workflows`, `infrastructure/temporal` | Approval gates; Temporal DB/schema setup and dynamic config |
| `apps/execution-worker` | Placeholder for later phases |
| `docs/decisions/` | Architecture decision records |

## Getting started

Prerequisites: Docker + Compose, Go 1.26+, Node 22+ with pnpm (for local dev only).

```bash
cp .env.example .env        # then edit: set WISE_POSTGRES_PASSWORD, WISE_REPO_ROOT,
                            # WISE_BOOTSTRAP_ADMIN_PASSWORD (12+ chars)
make up                     # build + start postgres, API and dashboard
open http://localhost:5173  # sign in with WISE_BOOTSTRAP_ADMIN_USER / _PASSWORD
```

The bootstrap admin is created only on first boot, when no users exist. You can also create users from the CLI:

```bash
docker compose exec -it wise-api wise-api user create -username alice
```

### Local development (API and dashboard on the host)

```bash
make setup     # pnpm install, create .env
make dev       # postgres + Temporal in Docker; API, orchestrator, runtime and Vite on the host
```

For `make dev`, `.env` must also set `WISE_DATABASE_URL` (pointing at `127.0.0.1:$WISE_POSTGRES_PORT`) and `WISE_REPO_ROOTS` — see `.env.example`.

### Common tasks

| Command | Purpose |
| --- | --- |
| `make gen` | Regenerate sqlc queries, Go server and TS types after changing SQL or `openapi.yaml` |
| `make test` | Go unit + integration tests (uses the compose Postgres) and dashboard typecheck + tests |
| `make migrate ARGS=status` | Migration status (`up`, `down`, `reset` also available) |
| `make lint` | `go vet`, `gofmt`, `tsc` |

## Agents and models

Agents ship as defaults in `packages/agent-definitions/agents/*.yaml` (validated against
`packages/contracts/agents/agent-definition.schema.json`). On an agent's page, **Configure** edits its prompt
(with a diff against the shipped one), its whole contract, model and limits; every save is a version you can
restore, and **Reset** returns to the shipped default. **New agent** and **Duplicate** create your own. Executable now: **product-agent**
and **requirements-agent**; the other nine are declared for later phases. Start a run from **Agents** in
the dashboard (optionally attaching a project so the agent can read its repositories) and watch the live
timeline of model calls, tool calls and the validated output.

Every model call goes through the **Model Gateway**; its aliases, providers and cache are edited in
**Settings → Models** (shipped defaults: `configs/models/models.yaml`):

| Alias | Default target | Use |
| --- | --- | --- |
| `reasoning` | `claude-code/sonnet` | analysis and planning agents |
| `deep` | `claude-code/opus` | hard problems |
| `local` | `lmstudio/<loaded model>` | fallback and lightweight work |

**LM Studio:** start the local server (Developer tab), load a chat model and enable *Serve on Local
Network* so Docker can reach it. Set `WISE_LMSTUDIO_MODEL` to pin a model; empty uses the loaded one.

**Claude Code:** run `claude setup-token` once and put the token in `.env` as `CLAUDE_CODE_OAUTH_TOKEN`
(`make dev` uses your existing login instead). Claude Code reports API-equivalent cost; the daily budgets
in **Settings → Policy & budgets** still bound usage, and that page lists which agents may use cloud models
at all. Every setting has a JSON mode, history and **Reset** to the shipped default; changes apply at once.

If a provider fails for good (e.g. not logged in), the gateway skips it for 60 s and uses the alias's
fallbacks. The **Models** page shows provider status, aliases and token/cost usage per agent.

## Board, workflows and accounts

**Board** — **Tasks** (`g t`) shows a project's lists like Trello (or all projects combined, read-only);
each project's board is also under Projects → a project → Board. Add cards and lists, drag both (mouse or
keyboard: focus, Space, arrows, Space), click a list name to rename it, and use ⋯ for its color, WIP limit
and **role**. Roles tell workflows where cards go: a run moves its card to the *in progress* list, a step
waiting for you to *review*, a finished task to the template's destination role. Click a card for a quick
view to edit it or run its workflow. New projects get the lists from Settings → Board.

**Task sources** — Board → Task sources imports Jira issues (JQL), ClickUp list tasks or Trello cards on a
schedule or with *Sync now*. Import only: Wise never writes back. Map source statuses to lists, pick the
workflow imported tasks use, and optionally run it automatically.

**Workflows** (`g w`) — templates of agent steps drawn as a flowchart. For each step choose the agent, the
account and model that runs it, and whether to pause for review (*Pass to next step* / *Request changes*;
changes re-run the step, or from an earlier one, with your comment as feedback). Insert steps with **+**
on a connection. Problems (e.g. a planner without a requirements step) are highlighted before you save.
On a task, pick a template, switch steps off or change their accounts for that task only, then **Run** or
**Run step by step** (pause after every step). A failed step waits; retry it, optionally on another account.

**Accounts** (Settings → Accounts) — add as many as you like: Claude Code (`claude setup-token`), Codex
(paste `~/.codex/auth.json`), LM Studio, DeepSeek or any OpenAI-compatible endpoint, plus Jira, ClickUp and
Trello for task sources. Secrets are encrypted with `WISE_SECRETS_KEY` and never shown again. Claude Code
and Codex run in the isolated `wise-cli-runner` container.

Temporal's UI (http://127.0.0.1:8233, namespace `wise`) shows every workflow's history. Commands to Temporal go
through a Postgres outbox, so decisions made while Temporal or the orchestrator is down are delivered later.
See `docs/decisions/0003-workflow-engine.md`, `0004-board-accounts-templates.md` and
`0005-panel-configuration.md`.

## API

Base URL `http://localhost:8080/api/v1` (also reachable via the dashboard at `/api/v1`). The live spec is served at `/api/v1/openapi.yaml`.

Authentication:
- **Dashboard:** `POST /auth/login` sets an `HttpOnly`, `SameSite=Strict` `wise_session` cookie and returns a `csrfToken`. State-changing requests must send it as `X-Wise-CSRF`.
- **Scripts/CLI:** create a personal token in *Settings* (or `wise-api token create -username admin -name cli`) and send `Authorization: Bearer wise_…`.

```bash
TOKEN=wise_...
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/projects
curl -s -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"name":"api","role":"backend","sourceType":"local","localPath":"/home/you/Projects/api"}' \
  localhost:8080/api/v1/projects/<projectId>/repositories
```

Errors are RFC 7807 `application/problem+json`, with per-field `errors` for validation failures.

## Security notes (Phase 1)

- Local repositories must live under `WISE_REPO_ROOTS`; paths are symlink-resolved and checked against the roots. The API container mounts `WISE_REPO_ROOT` **read-only**. Wise never modifies files in Phase 1.
- Git is invoked without a shell, with timeouts and `GIT_TERMINAL_PROMPT=0`. Credentials embedded in remote URLs are redacted when read and rejected when submitted.
- Passwords use argon2id; session and API tokens are stored only as SHA-256 hashes. Failed logins are rate-limited per client IP.
- Every mutation writes an `audit_logs` row in the same transaction.
- All published ports bind to `127.0.0.1`. Set `WISE_COOKIE_SECURE=true` when serving over HTTPS.
