"""Index-backed tools: they ask the control plane, which searches the run's project only."""

from __future__ import annotations

from typing import Any

from .base import ToolError
from .workspace import Workspace


def _remote(ws: Workspace, path: str, params: dict[str, Any]) -> dict[str, Any]:
    if ws.context is None:
        raise ToolError("the code index is not available to this run")
    if not ws.project:
        raise ToolError("no project is attached to this run; context tools need one")
    return ws.context(path, {k: v for k, v in params.items() if v not in (None, "")})


def search_context(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _remote(
        ws,
        "search",
        {
            "q": args["query"],
            "kind": args.get("kind"),
            "repository": args.get("repository"),
            "limit": args.get("limit"),
        },
    )


def find_symbol(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _remote(
        ws, "symbols", {"name": args["name"], "kind": args.get("kind"), "repository": args.get("repository")}
    )


def list_dependencies(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _remote(ws, "dependencies", {"repository": args.get("repository")})


IMPLEMENTATIONS = {
    "search_context": search_context,
    "find_symbol": find_symbol,
    "list_dependencies": list_dependencies,
}
