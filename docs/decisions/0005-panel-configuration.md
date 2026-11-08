# 0005 — Everything configurable from the panel (Phase 3c)

Status: accepted · 2026-11-08

## Context

The user requires Wise to be fully customizable from the dashboard: system
prompts, agents, workflow templates, models, policies, limits and the board
must never need a code or file edit. Until Phase 3b, agents and prompts were
embedded files synced on startup (overwriting anything else), models and
policies were YAML files, and every board had the same five fixed columns.

## Decisions

**Files ship defaults; the database is the source of truth.** Settings
(`models`, `model_policy`, `workflow_defaults`, `board_defaults`) live in
`app_settings`. A missing key is seeded once from its file under `configs/`;
afterwards the file is only the *default* that **Reset** restores. Each save is
validated with the same code that validates the files, versioned (optimistic
`version`; a stale editor gets 409), kept in `app_settings_history`, audited,
and applied live (the gateway swaps its configuration and providers under its
lock; workflow and board defaults are read at use). A stored value that no
longer validates after an upgrade is kept but the default is used, with a
warning, until it is fixed in Settings. Shipped defaults change a stored
setting only through a migration that checks the value is still the old,
unedited default.

**Agents are versioned rows.** Every save of an agent's contract and prompt is
a new `agent_definitions` row (`source = panel`, with the shipped hash it
started from). Startup sync still registers shipped definitions, but makes
them current only for agents without a panel edit; otherwise the newer shipped
version is kept and flagged ("newer shipped version"). Reset makes the latest
shipped row current; Versions can restore any row. Panel and shipped
definitions go through one `Validate` (schema, known tools, `produces` /
`consumes`, compilable schemas, a prompt for enabled agents, `-agent` names).
Plan resolution and runs read the current row, so an edit applies to the next
run. Shipped agents can be switched off but not deleted; a panel agent cannot
be deleted while a template uses it. Agent runs keep the exact definition row
they ran with.

**Board lists are per project, and workflows address them by role.** A list
may carry one role — `backlog`, `todo`, `in_progress`, `review`, `done`
(unique per project). Runs move cards to the in-progress list, then review,
then the template's destination role; a board without that role leaves the
card where it is. Templates are shared across projects, so they name roles,
not lists. Task-source status maps name list ids; unmapped statuses go to the
list with the matching role (else `todo`, else backlog/first). Deleting a list
moves its cards (and mappings) to a list the user picks. New projects get
`board_defaults`.

**The Tasks page is the board.** It reuses the project board component with a
remembered project choice. "All projects" combines lists by name and is
read-only, because lists belong to projects. Filtering pauses drag-and-drop,
since a new position is computed from neighbours that might be hidden.

## Kept out of the panel (on purpose)

Secrets (`WISE_SECRETS_KEY`, runtime/runner tokens, database password,
bootstrap admin password — account credentials are entered in the panel and
stored encrypted), infrastructure wiring (database URL, Temporal address,
networks, ports, image versions) and security boundaries (`WISE_REPO_ROOTS`,
runtime and CLI-runner isolation, session/CSRF rules). `CLAUDE.md` holds the
rule and this list; anything new defaults to configurable.

## Consequences

- Editing a file under `configs/` or `packages/` no longer changes a running
  install, except through Reset (or a guarded migration). This is intended.
- Settings changes cost one gateway reconfigure; in-flight calls finish on the
  old configuration.
- Custom lists make "Review" and "Done" labels unreliable; code must use roles.
