from __future__ import annotations

import logging
from typing import Any

from .client import ControlPlane

log = logging.getLogger(__name__)

MAX_STRING = 8_000


def _clip(value: Any, limit: int = MAX_STRING) -> Any:
    """Bound event payload size; full tool output stays in the transcript."""
    if isinstance(value, str):
        return value if len(value) <= limit else value[:limit] + f"… [{len(value) - limit} chars truncated]"
    if isinstance(value, dict):
        return {k: _clip(v, limit) for k, v in value.items()}
    if isinstance(value, list):
        clipped = [_clip(v, limit) for v in value[:200]]
        if len(value) > 200:
            clipped.append(f"… [{len(value) - 200} items truncated]")
        return clipped
    return value


class EventEmitter:
    """Buffers runtime events and sends them in batches."""

    def __init__(self, client: ControlPlane, run_id: str, token: str):
        self._client = client
        self._run_id = run_id
        self._token = token
        self._buf: list[dict[str, Any]] = []

    def emit(self, type_: str, payload: dict[str, Any]) -> None:
        self._buf.append({"type": type_, "payload": _clip(payload)})

    def flush(self) -> None:
        if not self._buf:
            return
        batch, self._buf = self._buf, []
        try:
            self._client.append_events(self._run_id, self._token, batch)
        except Exception:  # events are observability; never fail a run because of them
            log.warning("failed to send %d events", len(batch), exc_info=True)
