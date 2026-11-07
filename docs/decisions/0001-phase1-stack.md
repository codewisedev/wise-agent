# ADR 0001 — Phase 1 stack and conventions

- **Status:** Accepted
- **Date:** 2026-11-06

## Context

Phase 1 (Foundation) needs a control-plane API, persistence, authentication and a basic dashboard that later phases (agents, Temporal workflows, context engine) can build on without rework.

## Decisions

1. **Single Go module at the repository root (`module wise`).** Go apps live under `apps/*` and can share packages; `migrations/` and `packages/contracts/api` are embeddable Go packages. This replaces the originally planned `go.work` multi-module layout, which would have prevented embedding root-level migrations and the OpenAPI spec.
2. **Spec-first REST.** `packages/contracts/api/openapi.yaml` is the source of truth. `oapi-codegen` generates a strict chi server; `openapi-typescript` generates dashboard types. Requests are validated against the spec at runtime (`kin-openapi`) before reaching handlers.
3. **Data access:** `pgx/v5` + `sqlc` (type-safe queries from plain SQL) + `goose` (versioned SQL migrations embedded in the binary, applied on startup when `WISE_AUTO_MIGRATE=true`). IDs are UUIDv7 generated in Go.
4. **Errors:** domain services return `apperr.Error`; the HTTP layer renders RFC 7807 problem documents. Handlers only build success responses.
5. **Authentication:** single-user-first local accounts. argon2id passwords; opaque session tokens in an `HttpOnly`/`SameSite=Strict` cookie with a per-session synchronizer CSRF token (`X-Wise-CSRF`); personal API tokens (`wise_<id>_<secret>`) stored as SHA-256. Roles (`admin`, `member`) exist but are not yet enforced beyond authentication.
6. **Audit:** every mutation records an `audit_logs` row in the same transaction, attributed to the user or API token in the request context.
7. **Repository access:** registration of local repositories is restricted to allow-listed roots (`WISE_REPO_ROOTS`), mounted read-only into the API container at identical paths. Remote repositories are metadata-only until the Workspace/Git workers exist (Phase 5).
8. **Dashboard:** Vite + React + React Router + TanStack Query, plain CSS. TypeScript is pinned to 6.x because `openapi-typescript` relies on the JS compiler API that TypeScript 7 (native) no longer ships.
9. **Integration tests** run against the compose Postgres, creating a throwaway database per test (`WISE_TEST_DATABASE_URL`), rather than testcontainers — faster and avoids extra image pulls.

## Consequences

- Changing the API means editing `openapi.yaml` and running `make gen`; the compiler then flags every handler and client call that must change.
- Temporal, Ollama and agent services are intentionally absent from `docker-compose.yml` until their phases.
