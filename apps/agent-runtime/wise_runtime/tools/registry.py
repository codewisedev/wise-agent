from __future__ import annotations

import time
from typing import Any

import httpx
import jsonschema

from ..client import ApiError
from .base import ToolError
from .context import IMPLEMENTATIONS as CONTEXT_TOOLS
from .fs import IMPLEMENTATIONS as FS_TOOLS
from .workspace import Workspace
from .ws import IMPLEMENTATIONS as WS_TOOLS

IMPLEMENTATIONS = {**FS_TOOLS, **CONTEXT_TOOLS, **WS_TOOLS}


class ToolRegistry:
    """Tools available to one run: the definition's tools that this runtime implements."""

    def __init__(self, definitions: list[dict[str, Any]], workspace: Workspace):
        self.workspace = workspace
        self.definitions = {d["name"]: d for d in definitions}
        missing = [n for n in self.definitions if n not in IMPLEMENTATIONS]
        if missing:
            raise ValueError(f"runtime does not implement tools: {missing}")
        for d in definitions:
            effect = d.get("sideEffects")
            if effect in ("none", "read"):
                continue
            if effect in ("write", "execute") and workspace.task_workspace is not None:
                continue  # confined to the task's sandbox
            raise ValueError(
                f"tool {d['name']} ({effect}) needs a task workspace; run this agent in a workflow "
                "with a workspace step"
            )

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
        except ApiError as e:
            return None, f"control plane error {e.status}: {e.detail}", int((time.monotonic() - start) * 1000)
        except httpx.HTTPError as e:
            return None, f"control plane unreachable: {e}", int((time.monotonic() - start) * 1000)
