# WISE

## Personal AI Engineering Platform

**Version:** 1.0
**Deployment:** Local-first, self-hosted
**Architecture:** Modular Monolith + Distributed Workers
**Autonomy Model:** Human Approval at Critical Stages

---

# 1. Product Vision

Wise is a self-hosted AI Engineering Platform designed to operate as a personal AI software engineering team.

It manages multiple projects and repositories, understands existing codebases, refines product requirements, creates implementation plans, writes code, reviews changes, runs quality checks, and prepares Git commits and pull requests.

Wise should work like an engineering organization with specialized AI agents collaborating through structured workflows.

The system should continuously learn from project history, developer preferences, code review feedback, and successful implementations.

However, critical decisions remain under human control.

### Core objectives

- Manage multiple projects and repositories.
- Support monorepos and multi-repository architectures.
- Understand existing codebases and documentation.
- Convert product ideas into structured requirements.
- Create technical plans and architecture proposals.
- Implement backend and optionally frontend features.
- Perform independent code reviews and security checks.
- Run formatting, linting, tests, and quality gates.
- Maintain persistent personal and project memory.
- Learn from corrections and previous engineering decisions.
- Prepare commits, push changes, and create pull requests with approval.

---

# 2. Architectural Principles

Wise will follow these principles:

1. **Local-first:** Core services and project execution run locally.
2. **Model-independent:** No agent should depend directly on a specific AI provider.
3. **Human-in-the-loop:** Critical actions require explicit approval.
4. **Modular architecture:** Start with a modular monolith rather than premature microservices.
5. **Isolated execution:** Every coding task runs inside an isolated workspace.
6. **Persistent memory:** Engineering knowledge survives across tasks and sessions.
7. **Observable workflows:** Every agent action and decision is traceable.
8. **Reproducibility:** Workflows, artifacts, and quality results are versioned.
9. **Replaceable runtime:** Agent frameworks and external coding agents (Claude Code, Codex) can be added, replaced, or removed without redesigning the platform.
10. **Controlled self-improvement:** Wise can propose improvements but cannot silently change its own security boundaries.
11. **Containerized dependencies:** Wise builds and runs with only Docker on the host. Every tool a service or task needs lives in a pinned container image.
12. **Customizable from the panel:** Every behavioural setting — system prompts, agents, workflow templates, model providers and aliases, policies and budgets, limits, board columns, task sources, accounts — is configurable in the dashboard, never only in code or files. Code and files ship defaults that are seeded into the database and can be reset; the database is the source of truth. Each feature ships with its API, panel editor, validation, audit trail and reset-to-default. Only secrets, infrastructure wiring and security boundaries stay outside the panel (see `CLAUDE.md`).

---

# 3. Technology Stack

| Component             | Technology                                 |
| --------------------- | ------------------------------------------ |
| Core API              | Go                                         |
| Dashboard             | React + TypeScript                         |
| Agent Runtime         | Python                                     |
| Agent Framework       | OpenHands SDK adapter                      |
| External Coding Agents | Claude Code CLI, OpenAI Codex CLI (optional adapters) |
| Workflow Engine       | Temporal                                   |
| Main Database         | PostgreSQL                                 |
| Vector Search         | pgvector                                   |
| Execution Environment | Docker                                     |
| Git Isolation         | Git Worktree                               |
| Code Analysis         | Tree-sitter                                |
| Search                | ripgrep                                    |
| API                   | REST                                       |
| Live Updates          | WebSocket                                  |
| API Contracts         | OpenAPI                                    |
| Agent Contracts       | JSON Schema                                |
| Local Models          | Ollama / vLLM                              |
| Model Gateway         | Custom Provider Adapter / optional LiteLLM |

The agent framework must remain replaceable. Wise should own its agent contracts and workflow definitions instead of adopting a framework-specific architecture.

---

# 4. System Architecture

Wise consists of two main execution areas.

## 4.1 Control Plane

Responsible for managing the entire platform.

Components:

- API Gateway
- Project Manager
- Repository Manager
- Workflow Orchestrator
- Agent Registry
- Coding Runtime Registry
- Approval Manager
- Model Gateway
- Context Engine
- Memory Manager
- Engineering Profile Manager
- Audit and Observability

## 4.2 Execution Plane

Responsible for performing actual engineering work.

Components:

- Agent Runtime
- Coding Runtime Adapters (native, Claude Code, Codex)
- Runtime Supervisor
- Runtime Proxy (model credentials and budgets)
- Workspace Manager
- Docker Sandbox
- Git Worktree Manager
- Code Execution Worker
- Test Runner
- Security Scanner
- Git Operations Worker

The Control Plane coordinates work. The Execution Plane performs work in isolated environments.

The Agent Runtime must not have unrestricted access to the host machine or Docker daemon.

---

# 5. Monorepo Structure

```text
wise/
├── apps/
│   ├── api/                    # Go API
│   ├── dashboard/              # React + TypeScript
│   ├── orchestrator/           # Temporal workers
│   ├── agent-runtime/          # Python agent runtime
│   ├── execution-worker/       # Isolated execution broker and runtime supervisor
│   ├── git-worker/             # Trusted Git Operations Worker
│   └── runtime-proxy/          # Per-run model credentials, budgets, egress
│
├── packages/
│   ├── contracts/
│   │   ├── api/
│   │   ├── agents/
│   │   ├── events/
│   │   └── workflows/
│   │
│   ├── agent-definitions/
│   ├── prompts/
│   ├── skills/
│   ├── model-providers/
│   ├── coding-runtimes/        # Claude Code, Codex, native adapters
│   └── shared/
│
├── infrastructure/
│   ├── docker/
│   │   ├── versions.env        # single source of pinned versions
│   │   ├── toolchains/         # node, go, rust task toolchain images
│   │   └── runners/            # native, claude-code, codex runner images
│   ├── temporal/
│   ├── postgres/
│   ├── monitoring/
│   └── scripts/
│
├── configs/
│   ├── agents/
│   ├── models/
│   ├── coding-runtimes/
│   ├── policies/
│   └── workflows/
│
├── docs/
│   ├── architecture/
│   ├── decisions/
│   ├── api/
│   └── development/
│
├── migrations/
├── tests/
├── .env.example
├── docker-compose.yml
└── README.md
```

---

# 6. Project and Repository Management

Wise must support multiple projects, each containing multiple repositories and resources.

A project can contain:

- Backend repository
- Frontend repository
- Mobile application
- Shared libraries
- Infrastructure repository
- Documentation
- External services
- Database definitions

### Example

```text
Project: Salon Booking Platform

├── Backend
│   ├── NestJS
│   ├── Prisma
│   └── PostgreSQL
│
├── Frontend
│   ├── React
│   └── TypeScript
│
├── Infrastructure
│   ├── Docker
│   └── MinIO
│
└── Documentation
    ├── Product Requirements
    ├── Architecture
    └── API Documentation
```

Each repository should have its own configuration, indexing status, Git information, and execution settings.

Wise must understand relationships between repositories within the same project.

---

# 7. Engineering Profile

Wise needs a persistent engineering profile representing the developer's standards.

It should contain:

### Coding standards

- Naming conventions
- Code organization
- Type safety
- Error handling
- Logging
- Validation
- Documentation

### Architecture preferences

- Modular architecture
- Dependency management
- Database conventions
- API design
- Transaction management
- Security requirements

### Testing standards

- Unit tests
- Integration tests
- E2E tests
- Test coverage expectations
- Mocking conventions

### Supported technologies

- Node.js
- TypeScript
- NestJS
- React
- Vue
- Go
- Rust

The profile should distinguish between:

- **Rules:** Explicit requirements that Wise must follow.
- **Preferences:** Inferred patterns that guide recommendations.
- **Approved Examples:** Previously approved implementations that serve as references.

Inferred preferences must not automatically become mandatory rules.

---

# 8. Agent System

Wise will use specialized agents instead of one general-purpose agent handling everything.

| Agent               | Responsibility                       |
| ------------------- | ------------------------------------ |
| Product Agent       | Understand product requirements      |
| Requirements Agent  | Refine and structure requirements    |
| Planner Agent       | Break work into implementation steps |
| Architect Agent     | Design technical solutions           |
| Backend Agent       | Implement backend functionality      |
| Frontend Agent      | Implement frontend functionality     |
| Reviewer Agent      | Review code quality                  |
| Security Agent      | Identify security vulnerabilities    |
| QA Agent            | Validate functionality and tests     |
| Documentation Agent | Update technical documentation       |
| Release Agent       | Prepare commits and pull requests    |

### Agent Contract

Every agent must implement a standard contract.

```json
{
  "name": "backend-agent",
  "version": "1.0.0",
  "description": "Implements backend functionality",
  "responsibilities": [],
  "input_schema": {},
  "output_schema": {},
  "tools": [],
  "permissions": [],
  "model_config": {},
  "runtime": { "default": "native", "allowed": ["native", "claude-code", "codex"] },
  "execution_limits": {},
  "error_handling": {},
  "evaluation": {}
}
```

Each agent must declare:

- Its responsibilities
- Required inputs
- Expected outputs
- Available tools
- Permissions
- Model configuration
- Allowed coding runtimes
- Execution limits
- Error handling
- Evaluation criteria

Agents should return structured reports and artifacts.

The QA Agent must independently execute tests rather than simply trust the Backend Agent's claims.

Implementation agents (Backend, Frontend) may run on Wise's native agent runtime or delegate execution to an external coding agent runtime (see Section 9). Delegation does not change the agent's contract, permissions, or evaluation.

---

# 9. External Coding Agent Integration

Wise must support **Claude Code CLI** and **OpenAI Codex CLI** as optional, replaceable coding-agent runtimes.

External runtimes are execution backends, not decision-makers. They operate only inside a task workspace, within limits set by Wise. They never own workflow state, approvals, quality verdicts, or Git history.

### 9.1 Goals and non-goals

Goals:

- Use mature coding agents for the `IMPLEMENTATION` and fix-loop stages without coupling Wise to them.
- Select a runtime per project, per agent role, and per task.
- Enforce the same isolation, budget, and approval rules regardless of runtime.
- Produce normalized events and results so the dashboard, audit log, and self-improvement pipeline do not care which runtime ran.

Non-goals:

- External runtimes do not replace Wise's planning, review, QA, or approval stages.
- Wise does not drive the CLIs through interactive terminal sessions or screen scraping. Only documented non-interactive modes are used.
- External runtimes are not required. Wise must be fully functional with only its native runtime.

### 9.2 Runtime adapter architecture

Each runtime is integrated through a dedicated adapter that implements a common interface owned by Wise.

```text
Workflow (Temporal activity)
        |
        v
Agent Runtime ──> Coding Runtime Adapter (claude-code | codex | native)
        |                   |
        |                   | RunSpec (no secrets, no Docker access)
        v                   v
Execution Worker (trusted) ──> creates per-run sandbox container
        |                          |
        |                          +-- pinned CLI binary
        |                          +-- task workspace mounted at /workspace
        |                          +-- egress only to Wise Runtime Proxy
        |
        +──< stdout event stream (JSONL) ── normalized by the adapter
```

The Agent Runtime builds the run specification and the adapter. The trusted Execution Worker is the only component that starts containers. This preserves the rule that the Agent Runtime has no Docker socket access.

#### Adapter interface

```python
class CodingRuntimeAdapter(Protocol):
    id: str                      # "claude-code", "codex", "native"
    def capabilities(self) -> RuntimeCapabilities: ...
    def validate(self, spec: RunSpec, policy: RuntimePolicy) -> list[PolicyViolation]: ...
    def build_invocation(self, spec: RunSpec) -> SandboxInvocation: ...   # command, env, files
    def parse_event(self, raw_line: bytes) -> list[RuntimeEvent]: ...     # native -> normalized
    def classify_exit(self, exit_code: int, last_events: list[RuntimeEvent]) -> RunOutcome: ...
```

Adapters are stateless translators. Launching, supervision, cancellation, and cleanup belong to the Execution Worker so they behave the same for every runtime.

#### Runtime-specific mapping

| Concern               | Claude Code adapter                                                  | Codex adapter                                                  |
| --------------------- | -------------------------------------------------------------------- | -------------------------------------------------------------- |
| Non-interactive mode  | `claude -p` (print mode)                                             | `codex exec`                                                   |
| Event stream          | `--output-format stream-json`                                        | `--json` (JSONL events)                                        |
| Instructions          | Prompt + `--append-system-prompt`                                    | Prompt + generated `AGENTS.md` override / config               |
| Tool permissions      | `--allowedTools` / `--disallowedTools`, managed `settings.json`, hooks | `--sandbox`, `--ask-for-approval never`, `config.toml` profile |
| Turn / step limit     | `--max-turns`                                                        | Enforced by supervisor (time, tokens, events)                  |
| Model selection       | `--model`                                                            | `--model` / `-m`                                               |
| Model endpoint        | `ANTHROPIC_BASE_URL` → Wise Runtime Proxy                            | Custom model provider `base_url` → Wise Runtime Proxy          |
| Final structured output | Final result event + Wise result file                              | `--output-last-message` / `--output-schema` + Wise result file |

Flags above are indicative. Each adapter must pin a CLI version, and contract tests must verify the flags and event formats for that version before an upgrade is allowed.

The Python Claude Agent SDK may be used instead of shelling out to the CLI if it provides better event fidelity. That choice stays inside the adapter.

### 9.3 Run specification and structured instructions

Wise never passes free-form chat to an external runtime. Each run receives a versioned `RunSpec` validated against JSON Schema in `packages/contracts/agents/`.

```json
{
  "run_id": "run_01J...",
  "task_id": "task_...",
  "agent": "backend-agent",
  "runtime": "claude-code",
  "runtime_version": "pinned-version",
  "plan": { "id": "plan_...", "hash": "sha256:...", "steps": ["step_3", "step_4"] },
  "objective": "Implement booking cancellation endpoint",
  "acceptance_criteria": [],
  "engineering_rules": [],
  "context_refs": [],
  "constraints": {
    "allowed_paths": ["services/booking/**"],
    "protected_paths": [".github/**", "infrastructure/**", "**/*.env*"],
    "forbidden_actions": ["git_commit", "git_push", "dependency_add_without_note"]
  },
  "limits": { "wall_clock_s": 1800, "idle_s": 300, "max_turns": 60, "max_cost_usd": 3.0 },
  "output_contract": "implementation-report@1"
}
```

The adapter renders the `RunSpec` into the runtime's native format (prompt, system prompt, `AGENTS.md`, config files). The rendered instructions are stored as an artifact so every run can be reproduced and audited.

The run may only execute plan steps covered by a valid `PLAN_APPROVAL`. The plan hash is included so that a run against a superseded plan is rejected.

Repository-provided instruction files (`CLAUDE.md`, `AGENTS.md`, etc.) are treated as untrusted context. They may guide style but can never widen permissions, because enforcement happens outside the agent.

### 9.4 Isolation and permissions

External coding agents must not receive unrestricted host, Docker, secret, or Git access. The CLI's own sandbox and permission settings are defense in depth only. The **primary boundary is Wise's sandbox container**.

| Resource   | Policy                                                                                                                                              |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Host       | Runs in a per-run container as a non-root user, read-only root filesystem, dropped capabilities, `no-new-privileges`, CPU/memory/PID limits.        |
| Filesystem | Only the task workspace is writable. No host home directory, SSH keys, or global CLI config is mounted.                                              |
| Docker     | No Docker socket, no privileged mode, no nested containers. Services needed for tests are provided by the Execution Worker.                        |
| Network    | Default deny. Egress only to the Wise Runtime Proxy and to package registries explicitly allowed by project policy.                                |
| Secrets    | No provider API keys in the container. The Runtime Proxy injects credentials, enforces budgets, and logs usage. Project secrets are never exposed.  |
| Git        | No remotes, no credentials, no Git admin directory mounted. Changes are extracted as a diff by the Execution Worker.                              |
| MCP/tools  | No MCP servers by default. Allowed MCP servers and shell commands are declared in the runtime policy.                                              |

#### Git boundary

- The Workspace Manager creates the Git worktree on the trusted side and mounts **only the working tree** into the sandbox.
- Any commits, branches, or Git config created inside the sandbox are ignored.
- After the run, the Execution Worker computes the diff, checks it against `allowed_paths` and `protected_paths`, and records it as an artifact.
- Commit and push remain exclusively in the `COMMIT` and `PUSH` stages, executed by the Git Operations Worker after the required approvals.

#### Credential handling

The Wise Runtime Proxy sits between the sandbox and model providers:

- Issues a per-run token that is valid only for that run and expires when the run ends.
- Exchanges it for the real provider credential on the trusted side.
- Enforces per-run and per-task budgets and stops the run when exceeded.
- Records tokens and cost so external runs appear in the same cost analytics as native runs.

If a runtime cannot be routed through the proxy (for example, subscription-based login), it must be explicitly enabled per project and is marked as a reduced-isolation mode in the dashboard.

### 9.5 Configuration

Runtime selection and permissions are exposed through project and task configuration.

```yaml
# configs/projects/<project>/coding-runtimes.yaml
coding_runtimes:
  enabled: [native, claude-code, codex]
  default: claude-code
  per_agent:
    backend-agent: claude-code
    frontend-agent: codex
  fallback: native

  policies:
    claude-code:
      version: "<pinned-version>"
      model: "<model-id>"
      allowed_tools: [Read, Edit, Write, Glob, Grep, "Bash(npm test:*)", "Bash(npm run lint:*)"]
      network_allowlist: [registry.npmjs.org]
      mcp_servers: []
      limits: { wall_clock_s: 1800, max_turns: 60, max_cost_usd: 3.0 }
    codex:
      version: "<pinned-version>"
      model: "<model-id>"
      sandbox_mode: workspace-write
      network_allowlist: []
      limits: { wall_clock_s: 1800, max_cost_usd: 3.0 }
```

Rules:

1. Task configuration may select any runtime enabled for the project and may **narrow** its policy. It can never widen it.
2. Changing a project's runtime policy is a security-policy change. It is versioned, audited, and requires explicit user approval.
3. The Self-Improvement system may propose runtime or policy changes but cannot apply them (see Section 13).
4. The runtime and policy version used are part of the approved plan artifact. Changing them after `PLAN_APPROVAL` invalidates the approval.

### 9.6 Supervision and lifecycle

Every external run is a Temporal activity with heartbeats.

```text
PENDING -> PREPARING -> RUNNING -> COLLECTING -> SUCCEEDED
                           |             |
                           +--> CANCELLED |
                           +--> TIMED_OUT +--> FAILED
                           +--> POLICY_VIOLATION
```

The supervisor in the Execution Worker must:

- Enforce wall-clock, idle (no events), turn, and budget limits.
- Cancel cleanly: signal the process group, then destroy the container after a grace period.
- Heartbeat to Temporal so crashed workers are detected and runs are retried or failed.
- Detect policy violations (writes outside `allowed_paths`, blocked commands, denied egress) and stop the run.
- Always destroy the sandbox and revoke the run token, even on failure.

#### Failure classification

| Outcome            | Examples                                       | Default handling                     |
| ------------------ | ---------------------------------------------- | ------------------------------------ |
| `auth_error`       | Invalid or expired provider credential         | Fail, notify user, no retry          |
| `rate_limited`     | Provider 429                                   | Retry with backoff                   |
| `budget_exceeded`  | Cost or token limit reached                    | Fail, report partial diff            |
| `timeout`          | Wall clock or idle limit                       | Fail or retry once per policy        |
| `policy_violation` | Protected path change, denied command          | Fail, discard diff, flag for review  |
| `runtime_crash`    | Non-zero exit, malformed event stream          | Retry once, then fall back if set    |
| `incomplete`       | Agent finished without meeting output contract | Route to fix loop                    |

Fallback to another runtime is allowed only if it is configured and the fallback runtime's policy is equal or narrower.

### 9.7 Output, events, and completion

Adapters translate native output into Wise's normalized event schema in `packages/contracts/events/`:

```text
run.started
run.message            # assistant text, redacted
run.tool_call          # name, arguments summary, decision (allowed/denied)
run.tool_result
run.command            # shell command, exit code, duration
run.file_changed       # path, change type (observed, not trusted)
run.usage              # tokens, cost
run.warning
run.completed | run.failed | run.cancelled
```

The final result is built by Wise, not taken from the agent:

- **Diff and changed files:** computed by the Execution Worker from the workspace.
- **Summary and rationale:** taken from the agent's structured final report, validated against the output contract.
- **Quality:** not accepted from the agent. Formatting, linting, tests, security, and review run afterward in `QUALITY_CHECKS`.
- **Transcript:** the raw event stream is stored as a redacted artifact for audit and self-improvement.

### 9.8 Acceptance criteria for this capability

- The same task can run on `native`, `claude-code`, or `codex` by changing only configuration.
- A run cannot read files outside the workspace, reach unlisted hosts, access the Docker socket, or push to a remote. These are verified by automated escape tests.
- No provider or project secret appears in the sandbox environment, filesystem, or stored transcripts.
- Cancelling a task stops the external run and destroys its sandbox within a defined grace period.
- External runs appear in the timeline, cost analytics, and audit log like native runs.
- Upgrading a CLI version requires passing adapter contract tests.
- Claude Code and Codex are installed only in their runner images (Section 19), never on the host or in core service images.

### 9.9 Open decisions

- Whether to support subscription-based login for the CLIs, given that it cannot be routed through the Runtime Proxy.
- Whether the Codex CLI's built-in Linux sandbox works inside the Wise container, or should be relaxed with the container as the sole boundary.
- Which MCP servers, if any, Wise should expose to external runtimes (for example, a read-only Context Engine server).
- Whether to drive Claude Code through the CLI or the Claude Agent SDK.

---

# 10. Workflow State Machine

Wise uses Temporal to manage durable engineering workflows.

### Main workflow

```text
TASK_CREATED
     |
     v
PRODUCT_ANALYSIS
     |
     v
REQUIREMENTS_REFINEMENT
     |
     v
SCOPE_APPROVAL
     |
     v
PLANNING
     |
     v
ARCHITECTURE_REVIEW
     |
     v
PLAN_APPROVAL
     |
     v
WORKSPACE_PREPARATION
     |
     v
IMPLEMENTATION
     |
     +----> BACKEND_AGENT
     |
     +----> FRONTEND_AGENT (Optional)
     |
     v
QUALITY_CHECKS
     |
     +----> FORMAT
     +----> LINT
     +----> TESTS
     +----> SECURITY
     +----> CODE_REVIEW
     |
     v
QUALITY_GATE
     |
     +----> FAILED: FIX AND RETRY
     |
     v
FINAL_REPORT
     |
     v
FINAL_APPROVAL
     |
     v
COMMIT
     |
     v
PUSH_APPROVAL
     |
     v
PUSH
     |
     v
CREATE_PULL_REQUEST
     |
     v
COMPLETED
```

### Approval requirements

| Stage                  | Approval                               |
| ---------------------- | -------------------------------------- |
| Requirements and scope | Required                               |
| Implementation plan    | Required                               |
| Architecture changes   | Required when scope changes materially |
| Final code changes     | Required                               |
| Git push               | Required                               |
| Pull request creation  | Configurable                           |
| Merge                  | Always manual                          |

If requirements or the implementation plan change, the previous approval must become invalid.

Wise should calculate artifact hashes or use version identifiers to ensure approvals apply only to the exact version that was reviewed.

### Important Git security rule

Agents must not receive unrestricted Git credentials.

A trusted Git Operations Worker should execute push operations only after receiving a valid approval.

---

# 11. Context Engine

The Context Engine gives agents an understanding of the existing codebase.

It should combine several retrieval strategies.

### Components

1. Repository scanner
2. File and directory indexer
3. Tree-sitter AST parser
4. Symbol index
5. Dependency analyzer
6. Documentation indexer
7. Semantic search using pgvector
8. ripgrep-based exact search

### Context hierarchy

```text
Personal Context
      |
      v
Project Context
      |
      v
Repository Context
      |
      v
Task Context
      |
      v
Execution Context
```

The Context Engine must understand:

- Existing architecture
- Dependencies
- Important classes and functions
- Database models
- API endpoints
- Coding conventions
- Previous implementation decisions

Vector search is useful for finding relevant information, but it must never replace reading the actual source code.

Every code index should be associated with a Git commit SHA so that agents do not unknowingly work with outdated context.

---

# 12. Personal Memory System

Wise should maintain three major memory scopes.

### 12.1 Developer Memory

Stores long-term engineering preferences.

Examples:

- Preferred programming languages
- Coding conventions
- Architectural preferences
- Testing expectations
- Common corrections
- Preferred implementation patterns

### 12.2 Project Memory

Stores project-specific knowledge.

Examples:

- Architecture decisions
- Business requirements
- Database structure
- API conventions
- Known limitations
- Previous implementation decisions
- Project-specific rules

### 12.3 Wise System Memory

Stores information about Wise itself.

Examples:

- Agent performance
- Successful workflows
- Common execution failures
- Model performance
- Tool reliability
- Cost optimization
- Improvement proposals

### Memory lifecycle

```text
COLLECT
   |
   v
EXTRACT
   |
   v
CLASSIFY
   |
   v
CHECK DUPLICATES
   |
   v
CREATE MEMORY CANDIDATE
   |
   v
USER APPROVAL (When Required)
   |
   v
VERSIONED STORAGE
   |
   v
RETRIEVAL
```

Memory should be stored using PostgreSQL and pgvector.

Important rules and prompts should also be version-controlled in Git.

---

# 13. Continuous Self-Improvement

Wise should learn from its own engineering activities.

Potential improvement sources:

- User corrections
- Code review feedback
- Repeated implementation failures
- Test failures
- Security findings
- Agent performance metrics
- Model performance
- Task duration
- Token and API costs
- Successful implementation patterns

### Improvement process

```text
Collect Engineering Feedback
          |
          v
Analyze Patterns
          |
          v
Identify Improvement
          |
          v
Generate Proposal
          |
          v
Run Evaluation
          |
          v
User Approval
          |
          v
Apply Improvement
          |
          v
Monitor Results
```

Wise may automatically collect information and generate improvement proposals.

However, it must never automatically modify:

- Security policies
- Agent permissions
- Approval requirements
- Execution boundaries
- Secret management
- Git authorization rules
- Coding runtime permissions and policies

All important changes must be versioned and reversible.

---

# 14. Model Strategy and Cost Optimization

Wise should not depend on expensive cloud AI subscriptions.

It should support a hybrid model architecture.

### Local models

Use Ollama or vLLM for:

- Embeddings
- Classification
- Memory extraction
- Documentation processing
- Simple code analysis
- Lightweight agent tasks

### Cloud models

Use stronger models when necessary for:

- Product analysis
- Complex architecture
- Difficult implementation tasks
- Complex debugging
- Independent code reviews

### Model Gateway

All AI requests should pass through a unified Model Gateway.

```text
Agents
   |
   v
Model Gateway
   |
   +----> Ollama
   |
   +----> vLLM
   |
   +----> OpenAI
   |
   +----> Anthropic
   |
   +----> Other Providers
```

The gateway should support:

- Provider abstraction
- Model routing
- Token tracking
- Cost tracking
- Per-agent budgets
- Per-task budgets
- Request caching
- Retry policies
- Local fallback
- Model evaluation

The user should be able to configure which agents can use cloud models.

---

# 15. Database Architecture

PostgreSQL is the primary database.

pgvector provides semantic memory and context retrieval.

### Main entities

```text
projects
repositories
project_services
project_resources

tasks
requirements
plans
plan_steps
artifacts

agent_definitions
agent_runs
agent_events
tool_calls

coding_runtimes
runtime_policies
runtime_runs
runtime_run_artifacts

workflow_runs
workflow_steps
approvals

quality_runs
quality_findings

git_operations
audit_logs

knowledge_sources
knowledge_documents
knowledge_chunks

code_symbols
code_relationships

architecture_decisions

engineering_profiles
engineering_rules
engineering_preferences
approved_examples

memory_entries
memory_feedback
improvement_proposals
```

### Data ownership

- PostgreSQL stores Wise's application data.
- Temporal manages durable workflow execution.
- PostgreSQL may maintain a queryable mirror of workflow status.
- Transactional outbox patterns should be used when synchronizing database changes with external workflow events.

Artifacts, approvals, and important decisions should be versioned.

---

# 16. API Contracts

Base URL:

```text
http://localhost:8080/api/v1
```

### Projects

```http
POST   /projects
GET    /projects
GET    /projects/:id
PATCH  /projects/:id
DELETE /projects/:id
```

### Repositories

```http
POST   /projects/:id/repositories
GET    /projects/:id/repositories
POST   /repositories/:id/index
GET    /repositories/:id/status
```

### Tasks

```http
POST   /projects/:id/tasks
GET    /tasks
GET    /tasks/:id
POST   /tasks/:id/cancel
```

### Workflows

```http
POST   /tasks/:id/workflows
GET    /workflows/:id
GET    /workflows/:id/events
POST   /workflows/:id/retry
```

### Approvals

```http
GET    /approvals
GET    /approvals/:id
POST   /approvals/:id/approve
POST   /approvals/:id/reject
```

### Agents

```http
GET    /agents
GET    /agents/:id
GET    /agents/:id/runs
```

### Coding Runtimes

```http
GET    /runtimes
GET    /projects/:id/runtime-config
PATCH  /projects/:id/runtime-config      # requires approval
PATCH  /tasks/:id/runtime-config         # may only narrow project policy
GET    /runtime-runs/:id
GET    /runtime-runs/:id/events
POST   /runtime-runs/:id/cancel
```

### Engineering Profile

```http
GET    /profile
PATCH  /profile
GET    /profile/rules
POST   /profile/rules
```

### Quality

```http
GET    /tasks/:id/quality
GET    /quality-runs/:id
GET    /quality-runs/:id/findings
```

### Git Operations

```http
GET    /tasks/:id/git
POST   /tasks/:id/git/commit
POST   /tasks/:id/git/push
POST   /tasks/:id/git/pull-request
```

All API definitions should be documented through OpenAPI.

Agent inputs and outputs should use versioned JSON Schema contracts.

### Live updates

WebSocket events should include:

- Workflow status changes
- Agent started
- Agent completed
- External runtime run events
- Tool execution
- Approval required
- Test results
- Security findings
- Git operation status

---

# 17. Security and Execution Isolation

Security must be part of the architecture from the beginning.

### Execution isolation

Each task should receive:

- Dedicated workspace
- Dedicated Git Worktree
- Isolated Docker environment
- Resource limits
- Controlled network access
- Restricted filesystem access

### Secrets

- Never expose host secrets to agents.
- Never store API keys in repository files.
- Keep provider credentials in a protected configuration.
- Do not give agents unrestricted Git credentials.
- Use short-lived credentials where possible.

### Network access

Execution environments should use restricted egress.

Dependency installation and model-provider access should be controlled through explicit network policies.

### Docker security

The Agent Runtime must not receive unrestricted access to the Docker socket.

A trusted Execution Worker should manage sandbox creation and destruction.

### External coding agents

Claude Code and Codex run under the same isolation rules as native agents, with additional controls described in Section 9.4: per-run containers, no Docker socket, default-deny egress through the Runtime Proxy, no provider keys in the sandbox, and no Git metadata or credentials.

Repository content, including instruction files, must be treated as untrusted input to the agent. Enforcement must never depend on the agent following instructions.

---

# 18. Observability

Every workflow must be traceable.

Wise should record:

- Task ID
- Workflow ID
- Agent ID
- Coding runtime and CLI version
- Runtime policy version
- Model and provider
- Token usage
- Estimated cost
- Tool calls
- Execution duration
- Files modified
- Test results
- Approval decisions
- Git operations
- Errors and retries

The dashboard should provide a complete activity timeline for every task.

---

# 19. Containerization and Docker Compose Architecture

Wise must be buildable and runnable through Docker without requiring runtime tools to be installed manually on the host machine. The only host prerequisites are Docker Engine with BuildKit and Docker Compose v2.

### 19.1 Containerized runtime dependencies

Rules:

1. **Image owns its tools.** Every external CLI, package, SDK, or runtime tool required by a Wise service is installed in the Docker image of the service that uses it, and only that image.
2. **No host assumptions.** No Wise service or workflow may call a binary, read a config file, or rely on a package from the host. A task runs only with the dependencies available in its configured container image.
3. **Pinned, configurable versions.** All base images, OS packages that matter, language runtimes, and CLIs use pinned versions supplied as build arguments from one versions file.
4. **Optional integrations are opt-in.** Optional tools, including Claude Code CLI and OpenAI Codex CLI, are built only when enabled through configuration or a Compose build profile.
5. **External services stay external.** PostgreSQL, Temporal, Ollama, and model-provider APIs are separate services or endpoints reached over configured networks. They are never bundled into a Wise service image.
6. **Secrets only at runtime.** Credentials and API keys are never baked into images, build arguments, image layers, or source code.

### 19.2 Image catalog

| Image                         | Purpose                                     | Contains                                                        | Must not contain                       |
| ----------------------------- | ------------------------------------------- | --------------------------------------------------------------- | -------------------------------------- |
| `wise-api`                    | Go Core API                                 | Static Go binary, CA certificates, migration tool               | Database server, Git credentials       |
| `wise-orchestrator`           | Temporal workers                            | Go or Python worker binary, Temporal SDK                        | Temporal server                        |
| `wise-agent-runtime`          | Python agent runtime and adapters           | Python, pinned dependencies (lockfile), adapter code            | Coding-agent CLIs, Docker CLI          |
| `wise-execution-worker`       | Sandbox and runner lifecycle                | Worker binary, Docker/OCI client                                | Model-provider keys                    |
| `wise-git-worker`             | Trusted commit, push, PR creation           | Git, Git hosting CLI/SDK                                        | Agent code, model access               |
| `wise-runtime-proxy`          | Model credentials, budgets, egress          | Proxy binary                                                    | Workspace mounts                       |
| `wise-dashboard`              | React UI                                    | Static build served by a minimal web server                     | Node toolchain in the final stage      |
| `wise-indexer` (or in worker) | Context Engine indexing                     | Tree-sitter grammars, ripgrep                                   | —                                      |
| `wise-toolchain-<stack>`      | Base for task execution (e.g. node, go, rust) | Language runtime, package manager, formatter, linter, test tools | Coding-agent CLIs, secrets            |
| `wise-runner-native`          | Native agent task runs                      | `wise-toolchain-<stack>` + native runner                        | Secrets, Git credentials               |
| `wise-runner-claude-code`     | Claude Code task runs (optional)            | `wise-toolchain-<stack>` + pinned Claude Code CLI               | Provider keys, Git credentials         |
| `wise-runner-codex`           | Codex task runs (optional)                  | `wise-toolchain-<stack>` + pinned Codex CLI                     | Provider keys, Git credentials         |

Tools such as ripgrep, Tree-sitter, Git, and formatters are installed in each image that uses them. There is no shared "tools on the host" layer.

### 19.3 Version pinning

All versions live in one file, consumed as build arguments:

```dotenv
# infrastructure/docker/versions.env
GO_VERSION=<pinned>
PYTHON_VERSION=<pinned>
NODE_VERSION=<pinned>
POSTGRES_PGVECTOR_IMAGE=pgvector/pgvector:<pinned>@sha256:<digest>
TEMPORAL_IMAGE=temporalio/auto-setup:<pinned>@sha256:<digest>
RIPGREP_VERSION=<pinned>
CLAUDE_CODE_VERSION=<pinned>
CODEX_CLI_VERSION=<pinned>
```

Requirements:

- Base images are pinned by tag and digest. `latest` is not allowed.
- Language dependencies are installed from lockfiles (`go.sum`, `uv.lock`/`poetry.lock`, `package-lock.json`/`pnpm-lock.yaml`).
- CLI versions must match the version pinned in the coding-runtime policy (Section 9.5). The adapter refuses to run if the image's CLI version differs.
- Images carry OCI labels describing their contents, for example `wise.tools="node=<v>,claude-code=<v>"`. The Coding Runtime Registry reads these labels for compatibility checks.
- Version upgrades are made in `versions.env` through a reviewed change and must pass the adapter contract tests and image smoke tests.

Example runner Dockerfile:

```dockerfile
# infrastructure/docker/runners/claude-code.Dockerfile
ARG TOOLCHAIN_IMAGE
FROM ${TOOLCHAIN_IMAGE}
ARG CLAUDE_CODE_VERSION
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && claude --version
LABEL wise.tools.claude-code="${CLAUDE_CODE_VERSION}"
USER wise
```

The Codex runner follows the same pattern with `@openai/codex@${CODEX_CLI_VERSION}`.

### 19.4 Optional integrations and build profiles

Optional components are enabled through Compose profiles and matching configuration:

| Profile       | Builds / starts                                  | Also requires                                       |
| ------------- | ------------------------------------------------ | --------------------------------------------------- |
| (default)     | Core services, native runner, default toolchains | —                                                   |
| `local-models` | Ollama                                          | Model Gateway configured to use it                  |
| `claude-code` | `wise-runner-claude-code` images                 | `claude-code` enabled in a project's runtime config |
| `codex`       | `wise-runner-codex` images                       | `codex` enabled in a project's runtime config       |

```bash
docker compose --profile claude-code --profile codex build
docker compose up -d
```

Enabling a runtime in a project when its runner image is not built must fail with a clear configuration error, never fall back to the host.

### 19.5 Task execution images

Each repository declares the toolchain image used to format, lint, test, and run agents for its tasks:

```yaml
# repository execution settings
execution:
  toolchain_image: wise-toolchain-node:<pinned>
  setup_commands: ["npm ci"]
  quality_commands:
    format: "npm run format:check"
    lint: "npm run lint"
    test: "npm test"
```

- Runner images are built from the repository's toolchain image plus the selected runtime layer, so an external coding agent can run the same tests the QA Agent will run.
- Project dependencies are installed inside the task container, using only registries allowed by the network policy. Package caches may be shared through named volumes, but never through host directories.
- `WORKSPACE_PREPARATION` performs a preflight check: the image exists, its labels satisfy the repository's and runtime's requirements, and the required tools respond to a version check. A failed preflight stops the workflow before any agent runs.

### 19.6 Secrets at runtime

- Secrets are supplied at runtime through Docker secrets (files under `/run/secrets`) or an external secret store, not through `ENV` or `ARG` in a Dockerfile.
- Build-time credentials (for example, a private package registry token) use BuildKit secret mounts (`RUN --mount=type=secret,...`) so they never appear in a layer or in image history.
- Each secret is mounted only into the service that needs it. Model-provider keys go only to `wise-runtime-proxy`, Git hosting credentials only to `wise-git-worker`, and the database password only to services that use the database.
- Runner containers never receive secrets. They get only a short-lived per-run token from the Runtime Proxy (Section 9.4).
- `.env.example` documents required variables with placeholder values. Real `.env` files and secret files are git-ignored and excluded by `.dockerignore`.
- CI scans built images for leaked secrets.

### 19.7 Compose topology

```text
Wise
 |
 +---- PostgreSQL + pgvector          (external service, own image)
 |
 +---- Temporal + Temporal UI         (external service, own images)
 |
 +---- Wise API
 |
 +---- Wise Orchestrator
 |
 +---- Wise Agent Runtime
 |
 +---- Wise Execution Worker
 |      |
 |      +---- Per-run runner containers (native, Claude Code, Codex)
 |
 +---- Wise Git Worker
 |
 +---- Wise Runtime Proxy
 |
 +---- Wise Dashboard
 |
 +---- Ollama                          (optional, profile: local-models)
```

Networks:

- `wise-data`: internal network for PostgreSQL and Temporal. No external egress.
- `wise-internal`: Wise services.
- `wise-sandbox`: runner containers. Egress only to the Runtime Proxy and allowlisted registries.
- `wise-egress`: only the services that need to reach external endpoints (Runtime Proxy, Git Worker).

Endpoints for PostgreSQL, Temporal, and model providers are configured by environment variables, so any of them can be replaced by an external or managed instance without changing Wise images.

```yaml
services:
  postgres:
    image: ${POSTGRES_PGVECTOR_IMAGE}
    networks: [wise-data]
    secrets: [postgres_password]

  temporal:
    image: ${TEMPORAL_IMAGE}
    networks: [wise-data]

  temporal-ui:
    image: ${TEMPORAL_UI_IMAGE}

  wise-api:
    build:
      context: .
      dockerfile: apps/api/Dockerfile
      args: { GO_VERSION: "${GO_VERSION}" }
    environment:
      DATABASE_HOST: postgres
      TEMPORAL_ADDRESS: temporal:7233
    secrets: [postgres_password]
    networks: [wise-internal, wise-data]

  wise-orchestrator:
    build:
      context: .
      dockerfile: apps/orchestrator/Dockerfile

  wise-agent-runtime:
    build:
      context: .
      dockerfile: apps/agent-runtime/Dockerfile
      args: { PYTHON_VERSION: "${PYTHON_VERSION}" }

  wise-execution-worker:
    build:
      context: .
      dockerfile: apps/execution-worker/Dockerfile

  wise-git-worker:
    build:
      context: .
      dockerfile: apps/git-worker/Dockerfile
    secrets: [git_hosting_token]
    networks: [wise-internal, wise-egress]

  wise-runtime-proxy:
    build:
      context: .
      dockerfile: apps/runtime-proxy/Dockerfile
    secrets: [anthropic_api_key, openai_api_key]
    networks: [wise-internal, wise-sandbox, wise-egress]

  wise-dashboard:
    build:
      context: .
      dockerfile: apps/dashboard/Dockerfile

  ollama:
    image: ${OLLAMA_IMAGE}
    profiles: [local-models]

  # Build-only targets; started per run by the Execution Worker, never by `compose up`.
  runner-claude-code-node:
    image: wise-runner-claude-code-node:${CLAUDE_CODE_VERSION}
    build:
      context: .
      dockerfile: infrastructure/docker/runners/claude-code.Dockerfile
      args:
        TOOLCHAIN_IMAGE: wise-toolchain-node:${NODE_VERSION}
        CLAUDE_CODE_VERSION: ${CLAUDE_CODE_VERSION}
    profiles: [claude-code]

  runner-codex-node:
    image: wise-runner-codex-node:${CODEX_CLI_VERSION}
    build:
      context: .
      dockerfile: infrastructure/docker/runners/codex.Dockerfile
      args:
        TOOLCHAIN_IMAGE: wise-toolchain-node:${NODE_VERSION}
        CODEX_CLI_VERSION: ${CODEX_CLI_VERSION}
    profiles: [codex]

secrets:
  postgres_password:   { file: ./secrets/postgres_password }
  git_hosting_token:   { file: ./secrets/git_hosting_token }
  anthropic_api_key:   { file: ./secrets/anthropic_api_key }
  openai_api_key:      { file: ./secrets/openai_api_key }

networks:
  wise-data:     { internal: true }
  wise-internal: { internal: true }
  wise-sandbox:  { internal: true }
  wise-egress:   {}
```

This is a service skeleton, not a production-ready Compose file. Health checks, migrations, persistent volumes, resource limits, and full network policies must still be implemented.

The Execution Worker needs to create runner containers. It must reach the container engine through a restricted path (for example, a Docker socket proxy limited to container create/start/stop/remove on approved images, or a rootless engine), never an unrestricted host socket.

### 19.8 Verification

- CI builds every image from a clean environment that has only Docker installed.
- Each image has a smoke test that checks its required tools and versions (for example `claude --version`, `codex --version`, `rg --version`).
- An end-to-end test brings up the stack with `docker compose up` on a fresh machine and completes a sample task without any host-installed language runtime or CLI.
- Image scans check for leaked secrets, unpinned base images, and known vulnerabilities.

---

# 20. Implementation Roadmap

Every phase follows principle 12 (*Customizable from the panel*): a feature is not done until everything a user may want to change about it is editable in the dashboard, with validation, audit and reset-to-default.

## Phase 1 — Foundation

Build:

- Monorepo structure
- Go API
- PostgreSQL
- Database migrations
- Project management
- Repository management
- Docker Compose
- Per-service Dockerfiles and pinned `versions.env`
- Docker secrets wiring and `.env.example`
- Image smoke tests in CI
- Basic dashboard
- Authentication and authorization

**Goal:** Wise can manage multiple projects and repositories.

## Phase 2 — Agent Infrastructure

Build:

- Agent contracts
- Agent registry
- Python runtime
- Model Gateway
- Local model integration
- Cloud provider adapters
- Tool execution contracts
- Agent event system

**Goal:** Wise can execute specialized AI agents.

## Phase 3 — Workflow Engine

Build:

- Temporal integration
- Workflow state machine
- Approval manager
- Task management
- Workflow events
- Retry and recovery
- Audit logs

**Goal:** Wise can coordinate a complete engineering workflow.

## Phase 3b — Board, Accounts and Configurable Workflows

Built: Kanban board per project; task import from Jira, ClickUp and Trello; multiple encrypted accounts per model provider (Claude Code, Codex, LM Studio, DeepSeek, OpenAI-compatible) chosen per workflow step; workflow templates edited as a flowchart, per-task step overrides and step-by-step runs.

## Phase 3c — Customization

Build:

- Panel-managed settings (model providers and aliases, policies and budgets, workflow defaults, board defaults) with live reload, history and reset
- Agent editor: edit every agent's prompt and contract, create and duplicate agents, versions and reset to the shipped default
- Custom board columns per project with workflow roles (backlog, to do, in progress, review, done)
- Tasks section as Trello-style lists across projects

**Goal:** Wise is fully customizable from the dashboard; code and files only provide defaults.

**Status:** done — see `docs/decisions/0005-panel-configuration.md`. Lists also gained a `todo` role (where unmapped imports land).

## Phase 4 — Context and Memory

Build:

- Repository indexing
- AST parsing
- Symbol indexing
- Documentation indexing
- Semantic search
- Personal memory
- Project memory
- Engineering profile

**Goal:** Wise understands existing projects and remembers engineering decisions.

## Phase 5 — Engineering Pipeline

Build:

- Isolated workspaces
- Git Worktree management
- Backend Agent
- Frontend Agent
- Reviewer Agent
- Security Agent
- QA Agent
- Formatting and linting
- Test execution
- Quality gates
- Automated fix loops

**Goal:** Wise can implement and verify software changes.

## Phase 5b — External Coding Agents

Build:

- Coding runtime adapter interface and normalized event contract
- Runtime Supervisor in the Execution Worker
- Runtime Proxy with per-run credentials and budgets
- Claude Code adapter and runner image (`claude-code` build profile)
- Codex adapter and runner image (`codex` build profile)
- Toolchain images and workspace preflight checks
- Project and task runtime configuration
- Adapter contract tests and sandbox escape tests

**Goal:** Implementation agents can delegate to Claude Code or Codex under Wise's isolation and approval rules.

## Phase 6 — Self-Improvement

Build:

- Feedback collection
- Agent performance metrics
- Memory refinement
- Improvement proposals
- Evaluation pipelines
- Versioned prompts and skills
- Rollback mechanisms

**Goal:** Wise learns from previous engineering work.

## Phase 7 — Release and Dashboard

Build:

- Final approval interface
- Commit management
- Push authorization
- Pull request integration
- Workflow visualization
- Cost analytics
- Agent monitoring
- Project activity timeline

**Goal:** Wise can complete the engineering lifecycle under human supervision.

---

# 21. MVP Acceptance Criteria

The first complete MVP should support this scenario, on a machine where only Docker and Docker Compose are installed:

1. Create a project.
2. Add multiple repositories.
3. Index source code and documentation.
4. Configure an Engineering Profile.
5. Submit a product or engineering task.
6. Product Agent analyzes and refines requirements.
7. User approves the scope.
8. Planner and Architect generate an implementation plan.
9. User approves the plan.
10. Wise prepares isolated workspaces.
11. Backend Agent implements the changes, using the native runtime or the configured external coding agent.
12. Frontend Agent optionally implements frontend changes.
13. Wise runs formatting, linting, tests, security checks, and code review.
14. Failed quality checks trigger a controlled fix loop.
15. Wise generates a final implementation report.
16. User approves the final changes.
17. Wise creates a commit.
18. User approves the push.
19. Wise pushes the changes.
20. Wise optionally creates a pull request.
21. Engineering feedback and approved decisions are saved to memory.
22. Wise can generate improvement proposals based on the completed task.

---

# 22. Long-Term Vision

Wise should evolve into a persistent AI engineering organization.

It should understand the developer's engineering style, maintain knowledge across projects, coordinate specialized agents, and continuously improve its execution quality.

The long-term goal is not simply to build an AI coding assistant.

It is to build an autonomous engineering platform that can:

- Understand products.
- Design architectures.
- Plan development.
- Implement software.
- Review and test code.
- Maintain project knowledge.
- Learn from engineering feedback.
- Improve its own workflows.
- Operate across multiple projects and repositories.

**Wise should be capable of doing the work of an engineering team while keeping the developer in control of important decisions.**
