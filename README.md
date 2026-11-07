# Wise

Personal, local-first AI engineering platform. See [`implementation-plan.md`](implementation-plan.md) for the full vision and roadmap.

**Status: Phase 3 — Workflow engine.** Wise manages projects and repositories behind a login, executes specialized AI agents through a Model Gateway (LM Studio and Claude Code), and coordinates them in durable Temporal workflows with versioned artifacts and human approval gates. Context indexing and the coding pipeline arrive in later phases.

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
| `configs/models`, `configs/policies` | Model aliases/providers and usage policy (cloud allow-list, budgets) |
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

Agents are declared in `packages/agent-definitions/agents/*.yaml` and validated against
`packages/contracts/agents/agent-definition.schema.json` at startup. Executable now: **product-agent**
and **requirements-agent**; the other nine are declared for later phases. Start a run from **Agents** in
the dashboard (optionally attaching a project so the agent can read its repositories) and watch the live
timeline of model calls, tool calls and the validated output.

Every model call goes through the **Model Gateway** (`configs/models/models.yaml`):

| Alias | Default target | Use |
| --- | --- | --- |
| `reasoning` | `claude-code/sonnet` | analysis and planning agents |
| `deep` | `claude-code/opus` | hard problems |
| `local` | `lmstudio/<loaded model>` | fallback and lightweight work |

**LM Studio:** start the local server (Developer tab), load a chat model and enable *Serve on Local
Network* so Docker can reach it. Set `WISE_LMSTUDIO_MODEL` to pin a model; empty uses the loaded one.

**Claude Code:** run `claude setup-token` once and put the token in `.env` as `CLAUDE_CODE_OAUTH_TOKEN`
(`make dev` uses your existing login instead). Claude Code reports API-equivalent cost; the daily budgets
in `configs/policies/models.yaml` still bound usage. That file also lists which agents may use cloud
models at all — Wise never edits it itself.

If a provider fails for good (e.g. not logged in), the gateway skips it for 60 s and uses the alias's
fallbacks. The **Models** page shows provider status, aliases and token/cost usage per agent.

## Workflows

Create a **task** (Tasks → New task) and Wise runs the engineering workflow on Temporal:

```text
product analysis → requirements → SCOPE APPROVAL → planning → architecture review → PLAN APPROVAL → final report
```

- **Approvals** (sidebar badge, `g i`) pin the exact artifact versions you review. *Request changes*
  needs a comment; the agents revise using it and ask again. A newer artifact version invalidates
  an open approval. Gates and the revision limit are in `configs/workflows/engineering.yaml`.
- A failed agent stage parks the workflow in **awaiting retry** — fix the cause (e.g. load a better
  model) and press *Retry stage*. Cancelling a task stops its running agents.
- The **Temporal UI** at http://127.0.0.1:8233 (namespace `wise`) shows each workflow's history.
- Commands to Temporal go through a Postgres outbox, so decisions made while Temporal or the
  orchestrator is down are delivered when they come back. See `docs/decisions/0003-workflow-engine.md`.

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
