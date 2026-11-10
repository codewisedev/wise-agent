"""The external coding framework follows a session the control plane drives."""

from __future__ import annotations

import pytest

from wise_runtime.client import ApiError
from wise_runtime.framework.base import RunCancelled, RunContext, RunFailed
from wise_runtime.framework.external import ExternalCoding


class Events:
    def __init__(self) -> None:
        self.items: list[tuple[str, dict]] = []

    def emit(self, type_: str, payload: dict) -> None:
        self.items.append((type_, payload))


def ctx() -> RunContext:
    return RunContext(
        run={"id": "r"}, definition={}, prompt="", tools=None, events=Events(), model_turn=lambda m: {}
    )  # type: ignore[arg-type]


def test_events_are_forwarded_and_the_report_returned() -> None:
    polls = [
        {"events": [{"type": "run.tool_call", "tool": "Bash", "command": "npm test"}], "done": False},
        {
            "events": [{"type": "run.message", "text": "done"}],
            "done": True,
            "outcome": {"class": "succeeded", "output": {"summary": "ok"}, "usage": {}},
        },
    ]
    c = ctx()
    out = ExternalCoding(
        lambda: {"runtime": "claude-code", "mode": "subscription", "reducedIsolation": True},
        lambda w: polls.pop(0),
        lambda: None,
    ).run(c)
    assert out == {"summary": "ok"}
    types = [t for t, _ in c.events.items]  # type: ignore[attr-defined]
    assert types == ["external.started", "tool.call", "agent.message"]
    assert c.events.items[0][1]["reducedIsolation"] is True  # type: ignore[attr-defined]


def test_failures_and_retryability() -> None:
    def run(outcome):
        return ExternalCoding(
            lambda: {}, lambda w: {"events": [], "done": True, "outcome": outcome}, lambda: None
        ).run(ctx())

    with pytest.raises(RunFailed) as e:
        run({"class": "rate_limited", "message": "429", "retryable": True})
    assert e.value.retryable and "rate_limited" in str(e.value)
    with pytest.raises(RunFailed) as e:
        run({"class": "incomplete", "message": "no report"})
    assert not e.value.retryable


def test_start_refused_is_not_retried_and_cancel_stops_the_session() -> None:
    def refuse():
        raise ApiError(409, "the project has not enabled codex", {})

    with pytest.raises(RunFailed) as e:
        ExternalCoding(refuse, lambda w: {}, lambda: None).run(ctx())
    assert not e.value.retryable and "not enabled codex" in str(e.value)

    cancelled: list[bool] = []
    c = ctx()
    c.cancel.set()
    with pytest.raises(RunCancelled):
        ExternalCoding(
            lambda: {}, lambda w: {"events": [], "done": False}, lambda: cancelled.append(True)
        ).run(c)
    assert cancelled == [True]
