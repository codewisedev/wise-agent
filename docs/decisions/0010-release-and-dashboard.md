# 0010 — Release and dashboard (Phase 7)

Status: accepted · 2026-11-11

## Context

Phase 5 ends with an approved commit delivered as a local `wise/…` branch. Phase 7 (implementation-plan §10
PUSH_APPROVAL → PUSH → CREATE_PULL_REQUEST, §21 MVP steps 17–20) completes the lifecycle under human supervision,
and adds the dashboard views the plan lists. The user decided:
- **Hosts:** GitHub and GitLab (gitlab.com and self-hosted), behind one interface.
- **Credentials:** fine-grained tokens per account, stored encrypted, used only by a trusted git worker.
- **Pull requests:** a per-project policy (off / ask after the push / automatic), drafts by default.
- **Dashboard:** a release view on the task, agent monitoring, cost analytics and a project activity timeline.

§10: "Agents must not receive unrestricted Git credentials. A trusted Git Operations Worker should execute push
operations only after receiving a valid approval."

## Decisions

**`wise-git-worker` is the only component that pushes or opens pull requests.**
- **What it has:** the `git` network (API, orchestrator) and `git-egress`, a read-only mount of the workspaces
  volume, a hardened read-only container, and two tokens.
- **What it lacks:** Docker access, user repositories and the database.
- **Its input:** it receives only ids. A push gets an approval id; a pull request gets a push id (plus the approval
  id when the project asks).

**Grants make every operation single-purpose and approved.** The worker asks the API (internal route, its own
`WISE_GIT_WORKER_TOKEN`) for a grant. The API checks, in one transaction:
- the approval is approved and pins a `release_request` artifact;
- that artifact names the task's **delivered** commit;
- the branch is a `wise/…` branch other than the base branch;
- the remote is https on the account's host, and the account is enabled;
- the push has not already succeeded (one push record per approval; retries only after a failure).

Only then does it return the pinned target and the token for that one operation. Pull requests are granted only
for a successful push, under the project's policy (`ask` needs an approved pull request approval). Tokens therefore
reach only the API and the worker, in memory. The orchestrator holds a different token that can only call the
worker with ids; it cannot obtain grants.

**Create-only pushes, never a merge.** The worker:
1. fetches the approved commit from the workspace's executor-private `trusted.git` (read-only, with fsck) into a
   temporary repository;
2. pushes `<sha>:refs/heads/<branch>` with `--force-with-lease=<ref>:`, so the remote branch must not exist;
3. treats a branch already at that commit as pushed, and never moves an existing branch (the same rule as
   `Deliver`).

Hooks are off. The token travels in `GIT_CONFIG_*` environment variables, never in argv, on disk, in logs or in
results. The worker re-checks the rules itself. No code path calls a merge API.

**The release step.** A new step type after `deliver`:
1. Wise records exactly what the push covers (`release_request`: commit, branch, remote, account, base) and asks
   for the push approval. A new version supersedes a pending approval, so any change invalidates it (§10).
2. On approval, the push follows; then a pull request per policy (`<step>-pr` approval when asked).
3. Declining completes the task without a push.

Further details:
- **Report first:** the final report is built before the release step so it can be the pull request's
  description.
- **Sandbox:** stops right after delivery when a release step follows. The files, with `trusted.git`, stay until
  the task ends; the sweeper removes only finished tasks' workspaces.
- **Templates:** the shipped "Engineering → code" template includes the step for new installs; existing templates
  are offered "Add release step". Git accounts and remotes live on the repository, never in `StepDef`.
- **Phase 6's hard rule:** gains a case: improvement proposals may add only agent steps (a release step is git
  authorization).

**Commit message before final approval.** While the final approval of the code is pending, you can reword the
commit:
- the executor creates a commit with the same tree, parent and author in `trusted.git`;
- it moves `refs/wise/snapshot` atomically;
- the new changeset version supersedes the approval, which is asked again.

**Dashboard.**
- **Release panel on the task:** the exact push target, approve or decline, push and pull request state.
- **Monitor:** queued and running runs (native and CLI), stuck runs (lease expired or no activity for 10 minutes),
  workflows waiting for you, cancel.
- **Costs:** spend per day by project, agent, model, runtime or billing. Subscription usage (the API-equivalent cost
  CLI subscriptions report) is kept apart from pay-per-use and local; evaluation replays are apart; today's spend
  against daily budgets; costliest tasks. The chart uses a validated categorical palette per theme, with a table
  view.
- **Project activity:** tasks, approvals, workflows, failed runs, branches, pushes, pull requests and memories in one
  paged feed.

## Consequences

- Pushing needs a GitHub fine-grained token (Contents and Pull requests: read and write, only the repositories Wise
  pushes to) or a GitLab project access token. Repository Test is a read check; the first push proves write access.
- The internal git routes are on the API's port, protected by the worker token only. Escape checks verify that
  runners cannot reach the worker or the API, that the orchestrator holds no git token, and that an unapproved push,
  a wrong token or the orchestrator's token is refused.
- Subscription coding runs are marked from now on; earlier ones are classified by provider.
- Improvements applied are not shown in the project activity (they are not project-scoped).
- The release agent of the original agent list is not shipped (removed 2026-11-11; migration 00031 archives the copy
  existing installs registered, unless it was edited in the panel or a template uses it). Commit messages come from
  the implementing agent and pull request descriptions from the final report.
