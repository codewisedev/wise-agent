from __future__ import annotations

import time
from typing import Any

import jsonschema

from .base import ToolError
from .fs import IMPLEMENTATIONS
from .workspace import Workspace


class ToolRegistry:
    """Tools available to one run: the definition's tools that this runtime implements."""

    def __init__(self, definitions: list[dict[str, Any]], workspace: Workspace):
        self.workspace = workspace
        self.definitions = {d["name"]: d for d in definitions}
        missing = [n for n in self.definitions if n not in IMPLEMENTATIONS]
        if missing:
            raise ValueError(f"runtime does not implement tools: {missing}")
        for d in definitions:
            if d.get("sideEffects") not in ("none", "read"):
                # Write tools need an isolated workspace (Phase 5) and are refused here.
                raise ValueError(f"tool {d['name']} has side effects {d.get('sideEffects')!r}; not allowed")

    def execute(self, name: str, args: dict[str, Any]) -> tuple[dict[str, Any] | None, str | None, int]:
        """Returns (output, error, duration_ms). Errors are reported to the model, not raised."""
        start = time.monotonic()
        try:
            if name not in self.definitions:
                raise ToolError(f"unknown tool {name!r}")
            try:
                jsonschema.validate(args, self.definitions[name]["inputSchema"])
            except jsonschema.ValidationError as e:
                raise ToolError(f"invalid arguments: {e.message}") from e
            out = IMPLEMENTATIONS[name](self.workspace, args)
            return out, None, int((time.monotonic() - start) * 1000)
        except ToolError as e:
            return None, str(e), int((time.monotonic() - start) * 1000)
        except OSError as e:
            return None, f"filesystem error: {e.strerror or e}", int((time.monotonic() - start) * 1000)
