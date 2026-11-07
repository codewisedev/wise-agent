# ADR 0002 — Agent infrastructure (Phase 2)

- **Status:** Accepted
- **Date:** 2026-11-07

## Context

Phase 2 makes Wise able to execute specialized agents: contracts, a registry, a Python runtime, a Model
Gateway with local and cloud providers, tool contracts and an event system. The developer currently has
**LM Studio** (local models) and a **Claude Code subscription** (no API key).

## Decisions

1. **Wise owns the agent contract.** `packages/contracts/agents/agent-definition.schema.json` (JSON Schema
   2020-12) defines every agent: schemas, tools, permissions, model config, limits, error handling,
   evaluation. Definitions live in `packages/agent-definitions/agents/*.yaml`, prompts in
   `packages/prompts/<agent>/system.md`. All 11 agents are declared; `product-agent` and
   `requirements-agent` are executable now, the rest are `enabled: false` until their phase.
2. **Registry is content-addressed.** The API embeds and validates definitions at startup and registers a
   new `agent_definitions` row whenever the hash of definition + prompt + referenced tool contracts
   changes. Runs reference the exact row they ran with.
3. **Model Gateway lives in the Go control plane.** Provider credentials, routing (aliases with
   fallbacks), cloud policy, per-run and per-agent daily budgets, retries, a 60 s provider cooldown after
   non-recoverable failures, a cache for temperature-0 requests, and per-attempt accounting
   (`model_calls`) are all in one trusted place. Agents and the runtime never see provider keys.
4. **One agent-turn protocol for every provider.** The model answers with a JSON envelope
   (`call_tools` or `final`) enforced by structured output (LM Studio `response_format: json_schema`,
   Claude Code `--json-schema`). Tool use therefore works the same with or without native function
   calling. The runtime sends only messages; alias, schemas, tools and budgets come from the server-side
   definition, so a compromised runtime cannot change them.
5. **Claude Code provider.** `claude -p --output-format json` in an empty temp directory with built-in
   tools and MCP disabled, only project settings (none exist there), a replaced system prompt and no
   session persistence. `--bare` is deliberately **not** used: it disables subscription (OAuth) auth.
   In Docker the CLI authenticates with `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` and gets a
   dedicated empty config directory. Cost is the CLI's API-equivalent figure; budgets still apply.
6. **Dispatch through a Postgres queue** until Temporal (Phase 3): `SELECT … FOR UPDATE SKIP LOCKED`,
   60 s leases extended by heartbeats, a reaper that requeues (or fails) runs whose runtime vanished,
   and retryable failures requeued while attempts remain. Cancellation is immediate for queued runs and
   cooperative (via heartbeat) for running ones.
7. **Runtime authentication.** A shared `WISE_RUNTIME_TOKEN` only allows claiming. Every other runtime
   call uses a per-run token (stored hashed) that is bound to the run in the URL and dies with the run.
   Runtime routes reject user sessions and user routes reject runtime tokens.
8. **Python runtime with an `AgentFramework` seam.** The built-in WiseLoop validates final output
   against the output schema (re-prompting up to `invalid_output_retries`) and enforces turn, tool-call
   and time limits; the control plane validates the output again before accepting it. An OpenHands
   adapter can implement the same interface in Phase 5.
9. **Runtime isolation.** In Docker the runtime sits only on an `internal: true` network with the API:
   no internet, no database, no Docker socket, read-only repositories, read-only root filesystem, all
   capabilities dropped. Phase 2 tools are read-only and confined to the run's project repositories
   (realpath checks reject `..` and symlink escapes; `.git` is off limits).
10. **Events** (`agent_events`, gap-free per-run sequence numbers) are written in the same transaction as
    state changes and fanned out over a WebSocket (`/api/v1/events/ws?runId=`); clients re-sync by
    sequence number after reconnects.

## Consequences

- Adding a provider (Ollama, Anthropic API, Gemini) means implementing `modelgateway.Provider` and adding
  it to `configs/models/models.yaml`; agents keep referring to aliases.
- Write-capable tools are refused by the runtime until isolated workspaces exist (Phase 5).
- Temporal (Phase 3) replaces dispatch only; runs, events and the runtime API stay.
