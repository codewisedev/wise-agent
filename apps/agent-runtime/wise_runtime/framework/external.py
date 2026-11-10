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


# Normalized runtime events → the agent event types the timeline shows.
def _agent_event(ev: dict[str, Any]) -> tuple[str, dict[str, Any]] | None:
    kind = ev.get("type", "")
    if kind == "run.message":
        return "agent.message", {"thought": ev.get("text", "")}
    if kind == "run.tool_call":
        summary = ev.get("command") or ev.get("input", "")
        return "tool.call", {"tool": ev.get("tool") or "shell", "input": {"summary": summary}}
    if kind in ("run.tool_result", "run.command"):
        error = bool(ev.get("isError"))
        payload: dict[str, Any] = {
            "tool": ev.get("tool") or ("shell" if kind == "run.command" else "tool"),
            "status": "error" if error else "ok",
            "input": {"summary": ev.get("command", "")},
            "output": ev.get("output", ""),
        }
        if error:
            payload["error"] = (ev.get("output") or "failed")[:500]
        return "tool.result", payload
    if kind == "run.file_changed":
        return "file.changed", {"path": ev.get("path", ""), "change": ev.get("text") or ev.get("tool", "")}
    if kind in ("run.completed", "run.failed"):
        return "external.finished", {
            "ok": kind == "run.completed",
            "text": ev.get("text", ""),
            "usage": ev.get("usage"),
        }
    if kind == "run.warning":
        return "agent.warning", {"text": ev.get("text", "")}
    return None  # usage is recorded by the control plane


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
                    mapped = _agent_event(ev)
                    if mapped:
                        ctx.events.emit(*mapped)
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
