# 0003 — Workflow engine (Phase 3)

Status: accepted · 2026-11-07

## Context

Phase 3 (`implementation-plan.md` §10, §20) makes Wise coordinate a whole engineering workflow:
durable execution, human approvals that bind to exact artifact versions, retry and recovery,
live workflow events and audit logs. Phase 2 already runs single agents through a Postgres run
queue and a Python runtime.

## Decisions

**Temporal on the shared Postgres.** The official `temporalio/server` image runs with its whole
config from environment variables, in its own databases (`temporal`, `temporal_visibility`)
owned by an unprivileged `temporal` role. Two one-shot jobs create the role and databases
(`infrastructure/temporal/init-db.sh`, using the Postgres image's `psql`) and install or upgrade
the schemas (`setup-schema.sh`, `temporal-sql-tool` from `admin-tools`); both are idempotent and
run on every start. The Temporal frontend and UI are bound to loopback only (they have no auth).
Pinned versions live in `infrastructure/docker/versions.env`.

**The orchestrator is a subcommand of the API binary** (`wise-api orchestrator`) run as its own
`wise-orchestrator` container. Go's `internal/` rule would otherwise force moving the database,
agents and tasks packages out of `apps/api`. The container is on an internal network with only
Postgres and Temporal: no internet, no model credentials, no runtime token.

**The API never calls Temporal — a transactional outbox does.** Starting a workflow, an approval
decision, a retry and a cancel each write a `workflow_commands` row in the same transaction as
the domain change, plus `pg_notify`. The orchestrator's dispatcher claims commands with
`FOR UPDATE SKIP LOCKED` and marks them processed only after Temporal accepted them (backoff on
failure, polling every 5 s as a safety net). Nothing is lost while Temporal or the orchestrator
is down; this was verified by approving with Temporal stopped.

**Postgres holds a queryable mirror; Temporal owns execution.** The workflow writes
`workflow_runs`, `workflow_steps`, `workflow_events`, `artifacts` and `approvals` only through
idempotent activities, so the workflow code stays deterministic and replays safely. Workflow
events get gap-free sequence numbers per workflow and a `pg_notify` in the same transaction; the
API LISTENs and fans them out over the existing WebSocket (`?workflowId=` or `?stream=workflows`).

**Agent stages reuse the Phase 2 run queue.** The `RunStage` activity queues an agent run with an
idempotency key (`wf:<run>:<stage>:a<attempt>:r<revision>`), polls it and heartbeats. A crashed
orchestrator replays and reattaches to the same agent run (verified: no duplicate run). An agent
run that fails after its own attempts parks the workflow in `awaiting_retry` until a human
retries; cancelling a task cancels its workflow and, immediately, its queued or running agents.

**Approvals bind to exact versions.** An approval pins `{artifactId, kind, version, hash}` for
each artifact its gate covers (scope: product analysis + requirements; plan: requirements + plan
+ architecture review) and a combined hash. Approving requires sending that combined hash; the
server refuses (409) when the hash differs, an artifact has a newer version, or stored content no
longer matches its hash. A new artifact version supersedes open approvals that pinned the kind.
Rejection needs a comment, which the workflow passes to the agents as `feedback` with their
`previous_output`; the revised artifacts get a new approval round (limit: `max_revisions` in
`configs/workflows/engineering.yaml`, frozen into each run at start). Decisions, retries and
cancels require an interactive session — API tokens get 403 — and are audited.

**The database is the source of truth for decisions.** The approval signal only wakes the
workflow, which then reads the decision through an activity and marks it consumed.

## Consequences

- Phase 5 adds workspace, implementation, quality and git stages after `PLAN_APPROVAL`, guarded by
  `workflow.GetVersion` so running workflows keep replaying.
- The orchestrator still connects to Postgres as the `wise` owner role; a dedicated least-privilege
  role is follow-up work.
- Temporal's frontend has no authentication; it must stay on loopback/internal networks.
