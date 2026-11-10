"""External coding runtimes (Claude Code, Codex) behind Wise's contract.

The control plane renders the run for the CLI, starts it in the task's
runner and normalizes its events; this framework follows the session, turns
its events into agent events, honours cancellation and returns the report
Wise validated. It never talks to the CLI or a model provider itself.
"""

from __future__ import annotations

import contextlib
from collections.abc import Callable
from typing import Any

from ..client import ApiError
from .base import RunCancelled, RunContext, RunFailed

# Normalized runtime events → agent event types shown in the run timeline.
_EVENT_TYPES = {
    "run.message": "agent.message",
    "run.tool_call": "tool.call",
    "run.tool_result": "tool.result",
    "run.command": "tool.result",
    "run.file_changed": "file.changed",
    "run.usage": "usage",
    "run.warning": "agent.warning",
    "run.completed": "external.finished",
    "run.failed": "external.finished",
}


class ExternalCoding:
    name = "external"

    def __init__(
        self,
        start: Callable[[], dict[str, Any]],
        poll: Callable[[int], dict[str, Any]],
        cancel: Callable[[], None],
    ):
        self._start, self._poll, self._cancel = start, poll, cancel

    def run(self, ctx: RunContext) -> dict[str, Any]:
        try:
            started = self._start()
        except ApiError as e:
            # Policy, budget or setup problems: retrying cannot help (5xx may).
            raise RunFailed(
                f"could not start the coding runtime: {e.detail}", retryable=e.status >= 500
            ) from e
        ctx.events.emit(
            "external.started",
            {
                "runtime": started.get("runtime"),
                "mode": started.get("mode"),
                "reducedIsolation": started.get("reducedIsolation"),
                "cliVersion": started.get("cliVersion"),
            },
        )
        try:
            while True:
                ctx.check()
                try:
                    news = self._poll(20)
                except ApiError as e:
                    if e.status == 404:
                        raise RunFailed("the coding session was lost; retrying", retryable=True) from e
                    raise
                for ev in news.get("events", []):
                    kind = ev.get("type", "")
                    payload = {k: v for k, v in ev.items() if k != "type" and v not in (None, "", False)}
                    ctx.events.emit(
                        _EVENT_TYPES.get(kind, "agent.warning"), {"runtimeEvent": kind, **payload}
                    )
                if news.get("done"):
                    return self._result(news.get("outcome") or {})
        except (RunCancelled, RunFailed):
            self._safe_cancel()
            raise
        except Exception:
            self._safe_cancel()
            raise

    def _safe_cancel(self) -> None:
        # Best effort: the executor also stops the runner at its limits.
        with contextlib.suppress(Exception):
            self._cancel()

    @staticmethod
    def _result(outcome: dict[str, Any]) -> dict[str, Any]:
        cls = outcome.get("class")
        if cls == "succeeded" and isinstance(outcome.get("output"), dict):
            return outcome["output"]
        msg = outcome.get("message") or cls or "the coding runtime failed"
        raise RunFailed(f"{cls}: {msg}", retryable=bool(outcome.get("retryable")))
