# Orchestrator

The Temporal worker for the engineering workflow lives in the API module, because it reuses
the API's internal packages (database, tasks, agents). It runs as a separate process and container:

```bash
wise-api orchestrator     # Docker: the `wise-orchestrator` service; host: `make orchestrator`
```

Code: `apps/api/internal/workflow/` (workflow, activities, outbox dispatcher) and
`apps/api/internal/tasks/` (tasks, artifacts, approvals, Postgres mirror). See
`docs/decisions/0003-workflow-engine.md`.
