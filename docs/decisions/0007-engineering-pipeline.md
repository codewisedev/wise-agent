# 0007 — Engineering pipeline (Phase 5)

Status: accepted · 2026-11-10

## Context

Through Phase 4, agents could read code but never change it. Phase 5 lets Wise
implement and verify changes: an agent writes code, the repository's own
checks run, independent agents review the diff, failures are fixed in
rounds, and you approve the exact change before it reaches your repository.
These decisions were taken with the user:
- a private clone per task, not a git worktree inside the user's repository;
- a short-lived container per task, started by a trusted worker;
- delivery is a branch only; push and pull requests come later;
- Wise's own agents write the code; Claude Code and Codex as coders follow in Phase 5b.

## Decisions

**One trusted executor; agents never touch Docker or your repository.** The
`wise-executor` service is the only component that:
- starts containers. Its Docker access goes through a filtering socket proxy
  that allows containers, exec, images and networks.
- mounts the repository roots read-write, only to add branches.

The API and the orchestrator call it with a token; agents never can. The proxy
limits API endpoints, not container parameters. So the executor itself is
trusted code that builds every sandbox from a fixed template: no other mounts,
never privileged, never on the host network, and an image taken only from the
repository's settings. A compromised executor would mean root on the host; it
holds no model, account or database credentials.

**A clone per task, a branch named after it.** Preparing a workspace:
- clones the repository into a private volume (`git clone --no-hardlinks`) on
  `wise/<slug>-<id>`, from the repository's current or configured base branch;
- removes the remotes;
- keeps tracked credential-like files out of the clone;
- records the base commit.

The user's working copy, index and `.git` are never written while agents work.

**A sandbox per task.** It runs the repository's toolchain image, which must
include git (`make toolchains` builds Node, Go and Python ones):
- as the host user, so delivered objects belong to them;
- with a read-only root filesystem, no capabilities, `no-new-privileges`, and
  CPU, memory and process limits;
- mounting only its own subdirectories of the workspaces volume: other tasks'
  workspaces are invisible;
- on a private network whose only way out is the egress proxy.

Before any agent runs, a preflight checks that the tools respond and runs the
setup commands. A failure stops the workflow with the reason, and you can
retry it.

**Once an agent has run, the workspace is hostile.** An agent can plant
`.git/config` (fsmonitor, filters, external diff), hooks and symlinks. So
after the clone the executor never runs git or does file IO in a workspace:
- file operations, commands and commits run by `docker exec` inside the
  sandbox (`wise-sandbox` helper), and their output is data;
- a commit leaves the sandbox only as a `git bundle` (plain data). It is
  fetched, with object checks, into an executor-private repository that
  shares the user's objects;
- the diff you approve is computed there, from the commit itself, never from
  sandbox output.

**Delivery writes exactly what you approved.** The gate's review pins the
verified changeset (its commit SHA is in the pinned artifact's hash). The
deliver step:
- refuses unless the latest decision on that gate approved exactly the
  latest changeset;
- fetches that commit into your repository and creates the branch atomically;
- never moves an existing branch: if another commit holds the name, it adds
  `-2`, `-3`, and so on;
- leaves your checkout untouched.

**Egress: package registries only.** `wise-egress` allows HTTP(S) to the hosts
listed in Settings → Execution (npm, PyPI, Go proxy, crates, GitHub archives
by default). It refuses private, loopback, link-local and other special
addresses after DNS resolution, and logs what it denies. It holds only a
read-only token for the allowlist, because it joins every sandbox network.

**Secrets stay out of model context:**
- credential-like tracked files are removed from the clone;
- workspace reads and searches refuse secret names and token-bearing files;
- command output, diffs and logs are redacted for known token patterns;
- the changeset flags credential-like files or tokens for the reviewer.

This is defence in depth, not a boundary: a secret in git history or a
renamed file can still be read by `ws_run_command`. Keep secrets out of
repositories.

**The pipeline is steps in the workflow template.** Steps gain a type:
- `workspace`: needs an earlier approval;
- `quality`: runs the repository's checks;
- `gate`: checks and reviews. On failure, a fix round re-runs the
  implementing step with the failures and an optional `qa-agent` diagnosis,
  then the checks and reviews. After `max_fix_rounds` it waits for your
  retry. When everything passes, it snapshots the verified changeset;
- `deliver`: needs a reviewed gate.

"Engineering → code" is shipped as a template; everything is editable in the
workflow editor. Plans frozen before Phase 5 replay unchanged; a test replays
recorded histories.

**Agents:**
- `backend-agent` and `frontend-agent` write code with the `ws_*` tools.
- `reviewer-agent` and `security-agent` review the diff read-only.
- `qa-agent` diagnoses failures.

The control plane enforces scope: a run reaches only its workflow's task
workspace, writes need a write tool and commands an execute tool in the
agent's definition.

## Consequences

- **Disk:** each task holds a full clone. Workspaces of finished tasks are
  removed after `retention_days`, and sandboxes stop when a workflow ends.
- **Cost:** fix rounds and reviews multiply model calls; `max_fix_rounds` and
  the agents' budgets bound them.
- **Images:** toolchain images must contain git. Repositories need
  execution settings (Detect proposes them) before a workspace can be
  prepared.
- **Phase 5b and 6:** push and pull requests are the next delivery steps,
  behind approvals, through this same executor.
