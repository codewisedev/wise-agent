"""The seam between Wise and agent frameworks.

Wise owns the contract (definition, tools, events, results). A framework only
decides how to drive the model: the built-in WiseLoop today, an OpenHands
adapter later. Frameworks never talk to model providers directly — every model
call goes through `RunContext.model_turn`, i.e. the control plane's gateway.
"""

from __future__ import annotations

import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any, Protocol

from ..events import EventEmitter
from ..tools import ToolRegistry


class RunFailed(Exception):
    """The run cannot succeed. `retryable` asks the control plane to requeue it."""

    def __init__(self, message: str, *, retryable: bool = False):
        super().__init__(message)
        self.retryable = retryable


class RunCancelled(Exception):
    """Cancellation was requested by the user or the lease was lost."""


@dataclass
class RunContext:
    run: dict[str, Any]
    definition: dict[str, Any]
    prompt: str
    tools: ToolRegistry
    events: EventEmitter
    model_turn: Callable[[list[dict[str, str]]], dict[str, Any]]
    project: dict[str, Any] | None = None
    cancel: threading.Event = field(default_factory=threading.Event)
    deadline: float = float("inf")

    @property
    def run_id(self) -> str:
        return self.run["id"]

    @property
    def limits(self) -> dict[str, Any]:
        return self.definition.get("execution_limits", {})

    def check(self) -> None:
        if self.cancel.is_set():
            raise RunCancelled("run cancelled")
        if time.monotonic() > self.deadline:
            raise RunFailed(f"timeout after {self.limits.get('timeout_s')}s (execution_limits.timeout_s)")


class AgentFramework(Protocol):
    name: str

    def run(self, ctx: RunContext) -> dict[str, Any]:
        """Execute the agent and return output matching definition.output_schema."""
        ...
