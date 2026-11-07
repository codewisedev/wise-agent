# Wise

Personal, local-first AI engineering platform. See [`implementation-plan.md`](implementation-plan.md) for the full vision and roadmap.

**Status: Phase 1 — Foundation.** Wise can manage multiple projects and their repositories behind a login. Agents, workflows (Temporal), context indexing and the engineering pipeline arrive in later phases.

## What's here

| Path | What |
| --- | --- |
| `apps/api` | Go control-plane API (chi, pgx, sqlc, goose, oapi-codegen) |
| `apps/dashboard` | React + TypeScript dashboard (Vite, React Router, TanStack Query) |
| `packages/contracts/api/openapi.yaml` | **Source of truth** for the REST API; Go server and TS client types are generated from it |
| `migrations/` | goose SQL migrations (embedded into the API binary) |
| `apps/{orchestrator,agent-runtime,execution-worker}` | Placeholders for later phases |
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
make dev       # postgres in Docker, `go run` API on :8080, Vite on :5173
```

For `make dev`, `.env` must also set `WISE_DATABASE_URL` (pointing at `127.0.0.1:$WISE_POSTGRES_PORT`) and `WISE_REPO_ROOTS` — see `.env.example`.

### Common tasks

| Command | Purpose |
| --- | --- |
| `make gen` | Regenerate sqlc queries, Go server and TS types after changing SQL or `openapi.yaml` |
| `make test` | Go unit + integration tests (uses the compose Postgres) and dashboard typecheck + tests |
| `make migrate ARGS=status` | Migration status (`up`, `down`, `reset` also available) |
| `make lint` | `go vet`, `gofmt`, `tsc` |

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
