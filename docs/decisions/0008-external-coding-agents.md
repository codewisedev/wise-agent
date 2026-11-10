# 0008 — External coding agents (Phase 5b)

Status: accepted · 2026-11-10

## Context

Phase 5 let Wise's own agents implement changes in isolated task workspaces.
Phase 5b lets implementing steps delegate to **Claude Code** or **Codex**, with
the same isolation, budget and approval rules. Wise still owns the plan, the
checks, the reviews, the quality gate, the final approval and delivery; the CLI
only edits the workspace. These decisions were taken with the user:
- the Runtime Proxy for API keys, plus an explicit subscription mode;
- both CLIs, behind one adapter interface;
- a runner container per run.

## Decisions

**Runners from the sandbox's fixed template.** For each run, the executor
starts a runner container:
- the repository's toolchain image (which now includes bash and ripgrep, which
  the CLIs need);
- the task workspace, with **an empty tmpfs over `.git`**, so the CLI never sees
  or changes git metadata;
- **a brand-new home for the run**, written by the executor only before start.
  Its parent is never mounted, so nothing an earlier run planted is there;
- the pinned CLI binaries, read-only from the `wise-coding-clis` volume;
- the task's private network and the sandbox's limits.

The pinned CLI binaries are standalone musl builds of the versions in
`infrastructure/docker/versions.env`, so there is no image per toolchain.

The executor supervises the runner (wall-clock and idle limits, cancellation),
reads output files back by exec, and always removes the runner. A workspace has
one runner at a time: a retried run stops the previous one.

**Per-run egress grants.**
- The runner's `HTTPS_PROXY` carries a run token. Both CLIs send it as
  `Proxy-Authorization`; we verified this for the pinned versions before
  relying on it.
- `wise-egress` looks grants up synchronously at the executor (no lag) and
  caches them briefly.
- A subscription run is granted only its vendor's hosts, for that run only.
- Private addresses stay refused whatever the grant says.

**Two credential modes.**
- **Runtime Proxy (API keys).**
  - `wise-model-proxy` joins a run's private network only for that run. Runners
    reach it directly, so egress keeps refusing private addresses.
  - The runner holds a per-run token. The proxy swaps in the account's real key
    (Anthropic API key, or an OpenAI key for Codex), meters usage from JSON and
    server-sent events, refuses calls once the run's budget is spent, and
    reports usage, which is recorded like native model calls.
  - No provider key ever enters a sandbox.
- **Subscription (reduced isolation).** This answers §9.9's open question: it is
  allowed, but only when a project explicitly permits it.
  - The vendor credential (a `claude setup-token` token, a Codex `auth.json`) is
    in the runner. Any command the CLI runs, including repository scripts, can
    read it.
  - Package registries stay reachable.
  - Wise cannot stop a run mid-way on cost: Claude Code enforces
    `--max-budget-usd` itself; Codex has turn and time limits only.
  - The UI says so next to the toggle and marks such runs as reduced isolation.

**Adapters in Go, next to the executor** (§9.2; a deviation from "in the agent
runtime"). Adapters are stateless:
- they render a versioned **RunSpec**
  (`packages/contracts/agents/run-spec.schema.json`, never carrying the
  credential) into a runner request;
- they normalize the CLI's JSONL events and classify the outcome (§9.6 classes).

The API drives a session:
- it checks the setting, the project policy, the cloud policy and the agent's
  daily budget;
- it resolves the credential, starts the runner and streams events.

The Python runtime follows the session as an `external` framework, so leases,
heartbeats, cancellation and the timeline work as for native runs. The runtime
choice travels on the agent run, like the account override, so workflow commands
are unchanged and recorded histories still replay.

**The CLIs (§9.9 decisions):**
- **Claude Code:** the CLI (`claude -p --output-format stream-json`, not the
  SDK), with these settings:
  - `--setting-sources user`: only managed settings, never the repository's
    hooks or permissions;
  - `--strict-mcp-config`: no MCP servers;
  - built-in tools limited to Bash, Read, Edit, Write and NotebookEdit;
  - `--json-schema` for the report.
- **Codex:**
  - `codex exec --json` with `--output-schema`;
  - instructions in the run's `AGENTS.md`;
  - update checks and analytics switched off;
  - Wise's container is the sandbox by default; Codex's own sandbox is
    selectable.
- **No MCP servers** are exposed to external runtimes.
- **Repository instruction files** (`CLAUDE.md`, `AGENTS.md`) are untrusted
  guidance. They cannot widen anything, because enforcement is outside the agent.

**Wise builds the result:**
- the report is validated against the agent's output contract (otherwise
  `incomplete`);
- the diff is the gate's verified changeset;
- the checks and reviews run after the CLI.

**Protected paths** (execution setting; for example `.github/workflows/**`,
`.env*`, keys) fail the quality gate for every runtime.

**Policy.** Runtimes are off for every project until the project enables them.
Changing a project's runtime policy is audited with its old and new values.

## Consequences

- Live-verified on the stack:
  - Claude Code (subscription mode) implemented a task in a runner, passed the
    checks and reviews, and its approved change was delivered as a `wise/…`
    branch;
  - the escape checks pass (`make escape-check REPO=…`).
- Codex ran live in a runner through the Runtime Proxy with a deliberately
  wrong key (recorded fixture; classified `auth_error`). It still tries
  `chatgpt.com` and `github.com` despite its update and analytics switches;
  egress refuses them and the run is unaffected. A real Codex task needs a Codex
  account.
- Cancelling a run removes its runner within a second and revokes its grant.
- Rendered instructions (without the credential) are stored per run as a
  `runtime_instructions` artifact. The normalized events in the run timeline
  serve as the transcript (§9.7); no separate raw transcript is stored.
- A runtime the project has not enabled is refused when the task's workflow is
  configured or started, before anything runs.
- Deferred:
  - changing `coding_runtimes` after a plan approval does not invalidate that
    approval (§9.5 rule 4; the step's runtime itself is frozen in the plan);
  - the escape checks cover environment values but not the proxy-mode
    runner's files.
- Existing stacks: the `execution` setting stored before Phase 5b has no
  `protected_paths`. Add them in Settings → Execution, or reset that setting to
  its default.
- Proxy mode is verified end to end with a fake key: the runner reaches the
  proxy, which reaches Anthropic with the injected key.
- Upgrading a CLI means rebuilding `wise-coding-clis` and re-recording the
  contract fixtures (`apps/api/internal/coding/testdata`).
