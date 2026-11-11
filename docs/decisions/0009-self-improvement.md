# 0009 — Self-improvement (Phase 6)

Status: accepted · 2026-11-11

## Context

Phases 1–5b gave Wise configurable agents, workflows, memory and an engineering pipeline, but Wise did not learn
from its own results. Phase 6 (implementation-plan §13, plus the Wise system memory of §12.3) closes that loop:
measure, propose, evaluate, apply with approval, roll back. These decisions were taken with the user:
- metrics are computed continuously; the analysis runs on demand and as a weekly digest (editable, can be off);
- proposals are evaluated by replaying past cases, only when the user starts it, on a model the user chooses;
- proposals may change agent prompts, memory, model routing and budgets, and workflow templates;
- the user rates runs and artifacts (thumbs and a note), in addition to implicit signals.

## Decisions

**Signals come from what Wise already records.** Run status, retries and errors; `output.invalid` events; review
decisions and comments; quality-gate rounds; model calls (errors, latency, cost); durations; and the new `feedback`
table. A rejected review counts against the agents of the steps it re-runs (from the gate's `reviseFrom` to the
gate), not every agent in the approval; a failed gate round counts against the gate's implementing step. Feedback on
an artifact Wise made itself (a changeset, a quality report) stays unattributed.

**Evaluation replays never count as work.** `agent_runs.purpose` is `work` or `eval`. Replays and judge runs are
excluded from Insights, the run list, model usage, an agent's daily budget and ratings. They carry the source run's
project so context tools work, and they share the run queue, so they can delay real work (the UI says so).

**The analysis sees aggregates and what the user wrote.** The improvement agent gets metrics, clustered and
truncated error messages, the user's review comments and rating notes, current prompts of agents with problems,
memories with their retrieval counts (derived from the context frozen on every run, so there is full history and no
counter starting at zero), alias and budget settings, templates, and earlier proposals with their outcomes. It never
sees agent outputs, tool output or imported task text: an approved change is lasting, and those could carry
injected instructions into it.

**The hard rule is enforced in code, twice.** Every proposal is checked when it is created and again when it is
applied (`internal/improvement/rules.go`):
- prompt proposals change only the prompt text, through edits applied to the current prompt, and never target the
  improvement agent, the judge, or the reviewer and security agents (weakening a gate is an approval change);
- routing proposals are JSON patches limited to `/aliases/<alias>/targets…` in `models` (targets may set only
  `provider` and `model`) and `/daily_budget_usd/<agent>` in `model_policy`. They never touch providers,
  credentials, `cloud_allowed_agents` or retries, and the patched value must pass the setting's own validation;
- template proposals may change agents, account/model pins and add steps, but may not remove, disable or weaken
  review, gate, workspace, quality or deliver steps or the reviewer/security steps behind gates, narrow
  `reviewIncludes`, change `reviseFrom` or `fixFrom`, lower or newly set `maxRevisions`, change or introduce a
  runtime, or change the source or destination;
- memory proposals create, merge or archive memories; approving the proposal is the approval (no second inbox step).

Every proposal must cite evidence (runs, reviews, ratings or memories) that exists in the database. Duplicates of
open proposals, and changes applied, rejected or rolled back in the last 90 days, are refused; every refusal is
kept on the analysis with its reason. Pinned accounts (template pins, evaluation model choices) go through the
same cloud-policy check as aliases in the Model Gateway, so nothing here can bypass `cloud_allowed_agents`.

**System memory.** `improvement_analyses` and `improvement_proposals` keep what was analysed, proposed, refused,
evaluated, applied and rolled back, with the user's notes, the metrics when a change was applied and since. The
next analysis receives these, so it does not repeat rejected ideas and can see whether applied ones helped.

**Staleness.** A proposal records the versions it was written against (agent definition, setting version, template
version, memory versions). If the target changed, the proposal becomes stale and can only be rejected.

**Replay evaluation.** An agent's cases come from its past runs: approved or thumbs-up outputs are *golden*, outputs
rejected with a comment are *must fix* (the comment is the expectation), and the user can add or switch off cases.
An evaluation runs each case on the current version and on the proposal:
- prompt proposals use a *candidate* definition version, never current, hidden from version lists and Restore. It
  reuses an existing row when the content hash matches, and becomes a real version only when the proposal is
  applied (the same row is reused, so what was evaluated is what is applied);
- routing proposals compare the old and new first target of the alias an affected agent uses.

Scoring:
- deterministic checks: contract validity, invalid attempts, empty required fields, secrets;
- the `eval-judge-agent` scores both outputs 1–5 against the agent's evaluation criteria and the case's
  expectation. It sees them as A and B in random order.

Results are shown per case with counts, and no verdict is drawn from a handful of cases. A cost cap stops an
evaluation; the user can stop it. Agents that need a task workspace (`ws_*` tools) are not replayed yet, and memory
and template proposals are not evaluated; they are judged by evidence and diff.

**Apply and roll back through existing versioned mechanisms.**

| Kind | Apply | Roll back |
| --- | --- | --- |
| Prompt | a new agent version (patch version raised, definition otherwise unchanged) | Restore |
| Routing | the setting, with its version check | the previous value |
| Template | a template save (`workflow_template_versions` now keeps every replaced definition, with History/Restore in the editor) | Restore |
| Memory | create active memories, archive others | archive what was created, reactivate what was archived |

If the target changed after applying, rollback asks for confirmation. Every step is audited.

**Two new shipped agents.** `improvement-agent` and `eval-judge-agent` are editable like any agent, never get memory
context, and are not added to `cloud_allowed_agents` by Wise: whether they may use cloud models is the user's policy
decision (otherwise they use the local fallback).

**Deferred.** "Versioned prompts and skills": prompts are versioned agent definitions; Wise has no skills yet.
Replaying workspace agents (coding, review) needs recorded workspaces and is left for later.

## Consequences

- The improvement loop can never widen what agents may do; at most it changes how they are instructed, which
  model answers, what they remember, and the shape of workflows within the approval rules.
- Evaluations cost model calls only when the user starts them; analyses cost one agent run per week (or none when
  there is too little data: the period is then recorded as skipped).
- With little history, analyses may honestly propose nothing.
