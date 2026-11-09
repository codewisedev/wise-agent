# 0006 — Context and memory (Phase 4)

Status: accepted · 2026-11-09

## Context

Agents could only grep and read repositories, and Wise forgot everything
between tasks. Phase 4 lets Wise understand existing projects (a code index
with keyword and semantic search) and remember engineering decisions (memory
and an engineering profile). Decisions taken with the user: embeddings use a
cloud provider by default but only with explicit consent, and every extracted
memory needs the user's approval.

## Decisions

**The indexer has no database access.** `wise-indexer` (same image as the agent
runtime, read-only repository mount, no egress) claims leased index jobs with
the runtime token and uploads files, tree-sitter symbols, chunks and
dependencies through the runtime API. The control plane alone writes the
index. Everything is tied to the commit the indexer read; results say when
HEAD has moved since (`stale`). Only changed files are re-parsed; a fingerprint
of the indexing settings is folded into each file's hash, so changing a
setting (e.g. adding a secret pattern) re-processes everything. HEAD changes
queue a re-index automatically.

**Secrets never enter the index.** Files named like credentials (`.env*`,
keys, certificates, `.npmrc`…) and files containing token patterns or
high-entropy secret assignments are recorded as skipped and never chunked,
embedded or shown — including through symlinks. Readable test placeholders
are not flagged. Users can add patterns but not remove the built-in guard.

**Search returns locations, not truth.** Keyword search splits identifiers
(`ReconfigureGateway` ↔ "gateway reconfigure"), boosts definitions, demotes
tests and falls back to any-word matches for questions. With an embedding
model configured, semantic results are fused with keyword results by
reciprocal-rank fusion. Agents get `search_context`, `find_symbol` and
`list_dependencies`, scoped to their run's project, and still read the real
file with `read_file`.

**Embeddings are a configured target with consent.** The `models` setting
names an OpenAI-compatible account or provider, model and dimensions; a cloud
provider additionally needs `allow_cloud`. Vectors are stored per model key;
changing the model re-embeds. Budgets and usage logging apply. A dimension
mismatch is recorded once and parks embedding until the configuration changes,
so paid calls are not repeated. Without embeddings, everything still works
with keyword search.

**Memory is approved knowledge.** Memories (rule, preference, decision, fact,
convention, example) belong to the developer or a project and are versioned.
The memory agent proposes candidates from review comments, final reports and
on request; they wait in an inbox (duplicates flagged) and influence no agent
until approved. Candidates and the extraction's "processed" mark are stored in
one transaction. Every agent run starts with a "Wise context" section: all
active rules, the most relevant memories and a project summary — computed once
at claim, stored on the run and shown on its page. The agents that produce
memory never receive it.

**The engineering profile is the developer's memory.** Rules, preferences and
conventions by area, plus approved examples (project-scoped, with the snippet,
location and commit, so they are useful even where the repository is not
readable). The profile agent infers preferences from a project's index
(dependencies, configuration, test layout, naming) — reading only
secret-guarded chunks — and its proposals are forced to preferences on the
server: making something a rule is always the user's explicit, versioned edit.

## Consequences

- A fresh install has keyword search immediately; semantic search starts when
  the user picks an embedding account and gives consent.
- Agent prompts grow by up to `max_context_chars` (Settings → Memory).
- Remote repositories are not indexed until they can be cloned (Phase 5).
- Wise system memory (§12.3) is left for Phase 6 (self-improvement).
