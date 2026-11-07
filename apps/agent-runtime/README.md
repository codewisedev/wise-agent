# Agent Runtime

Python runtime that executes Wise agents (implementation-plan.md §8).

- Claims queued runs from the control plane (`/api/v1/runtime/v1`), one at a time.
- Drives the agent with **WiseLoop** behind the `AgentFramework` seam (`wise_runtime/framework`);
  an OpenHands adapter can be added later without changing the contract.
- Every model call goes through the control plane's Model Gateway — the runtime holds **no provider
  credentials**, has **no database access**, and in Docker sits on an internal network with **no internet**.
- Tools are read-only and confined to the run's project repositories (`wise_runtime/tools`):
  `get_project_context`, `list_files`, `read_file`, `search_code` (ripgrep).

```bash
uv sync
uv run pytest
WISE_API_URL=http://127.0.0.1:8080 WISE_RUNTIME_TOKEN=... WISE_REPO_ROOTS=/home/you/Projects uv run python -m wise_runtime
```
