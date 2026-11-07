from __future__ import annotations

import threading

import httpx

from wise_runtime.client import ControlPlane
from wise_runtime.config import Config
from wise_runtime.framework import RunCancelled
from wise_runtime.worker import Worker

from .conftest import FakeControlPlane, final, tools

GOOD = {"summary": "booking app", "features": ["calendar"]}


def worker(fake, roots=()):
    return Worker(
        Config(api_url="http://x", runtime_token="t", runtime_id="rt", repo_roots=roots), client=fake
    )


def test_tool_round_then_final(repo_tree):
    fake = FakeControlPlane(
        [tools(("get_project_context", {}), ("search_code", {"pattern": "CreateBooking"})), final(GOOD)],
        project=repo_tree["project"],
    )
    assert worker(fake, (repo_tree["root"],)).poll_once() is True
    assert fake.completed == GOOD and fake.failed is None
    assert fake.event_types() == [
        "agent.message",
        "tool.call",
        "tool.result",
        "tool.call",
        "tool.result",
        "agent.message",
    ]
    second = fake.transcripts[1]
    assert [m["role"] for m in second] == ["user", "assistant", "tool", "tool"]
    assert "Salon" in second[0]["content"] and "booking.go" in second[3]["content"]
    assert worker(fake).poll_once() is False  # queue empty


def test_invalid_output_is_corrected_once():
    fake = FakeControlPlane([final({"summary": "x"}), final(GOOD)])
    worker(fake).poll_once()
    assert fake.completed == GOOD
    assert "features" in fake.transcripts[1][-1]["content"]
    assert "output.invalid" in fake.event_types()


def test_too_many_invalid_outputs_fail_the_run():
    fake = FakeControlPlane([{"action": "invalid", "parseError": "no JSON", "rawText": "hi"}] * 2)
    worker(fake).poll_once()
    assert fake.completed is None
    assert fake.failed["retryable"] is False and "invalid" in fake.failed["error"]


def test_max_turns_and_tool_limit():
    loop = [tools(("get_project_context", {}), ("get_project_context", {}))] * 6
    fake = FakeControlPlane(loop)
    worker(fake).poll_once()
    assert "max_turns" in fake.failed["error"]
    results = [e["payload"] for e in fake.events if e["type"] == "tool.result"]
    assert sum(r["status"] == "ok" for r in results) == 3  # max_tool_calls
    assert "limit reached" in results[-1]["error"]


def test_rejected_output_is_not_retried():
    fake = FakeControlPlane([final(GOOD)])
    fake.reject_output = True
    worker(fake).poll_once()
    assert fake.failed["retryable"] is False and "422" in fake.failed["error"]


def test_cancellation_is_reported():
    fake = FakeControlPlane([tools(("get_project_context", {}))], cancel_after=1)
    w = worker(fake)

    class CancelOnTool(type(w.framework)):
        def run(self, ctx):
            threading.Timer(0, ctx.cancel.set).run()
            ctx.check()
            raise AssertionError("unreachable")

    w.framework = CancelOnTool()
    w.poll_once()
    assert fake.failed == {"error": "cancelled by user", "retryable": False, "cancelled": True}


def test_unsupported_tool_fails_once():
    fake = FakeControlPlane([final(GOOD)])
    claim = fake.claim("rt")
    fake.claimed = False
    claim["tools"] = [{**claim["tools"][0], "name": "write_file", "sideEffects": "write"}]
    fake.claim = lambda _rid: claim
    worker(fake).poll_once()
    assert fake.failed["retryable"] is False and "cannot run this agent" in fake.failed["error"]


def test_crash_is_retryable():
    fake = FakeControlPlane([{"action": "final", "final": GOOD}])

    class Boom:
        name = "boom"

        def run(self, ctx):
            raise KeyError("oops")

    w = worker(fake)
    w.framework = Boom()
    w.poll_once()
    assert fake.failed["retryable"] is True and "KeyError" in fake.failed["error"]
    assert isinstance(RunCancelled("x"), Exception)


def test_client_sends_bearer_tokens_and_maps_errors():
    seen = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append((request.url.path, request.headers["authorization"]))
        if request.url.path.endswith("/claim"):
            return httpx.Response(204)
        return httpx.Response(
            429,
            json={
                "title": "Too Many Requests",
                "detail": "budget exceeded",
                "errors": [{"field": "x", "message": "y"}],
            },
        )

    c = ControlPlane("http://api", "svc-token", transport=httpx.MockTransport(handler))
    assert c.claim("rt") is None
    try:
        c.model_turn("r1", "wrt_abc", [{"role": "user", "content": "hi"}])
        raise AssertionError("expected ApiError")
    except Exception as e:
        assert e.status == 429 and "budget exceeded" in e.detail and "x: y" in e.detail
    assert seen == [
        ("/api/v1/runtime/v1/runs/claim", "Bearer svc-token"),
        ("/api/v1/runtime/v1/runs/r1/model-turn", "Bearer wrt_abc"),
    ]
