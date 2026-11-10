"""Task workspace tools: they ask the control plane, which works in the run's task sandbox."""

from __future__ import annotations

from typing import Any

from .base import ToolError
from .workspace import Workspace


def _ws(ws: Workspace):
    if ws.task_workspace is None:
        raise ToolError("this run has no task workspace; workspace tools need one")
    return ws.task_workspace


def _fs(ws: Workspace, req: dict[str, Any]) -> dict[str, Any]:
    out = _ws(ws)("fs", {k: v for k, v in req.items() if v not in (None, "")})
    if out.get("error"):
        raise ToolError(out["error"])
    return out


def ws_list_files(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _fs(
        ws, {"op": "list", "path": args.get("path"), "glob": args.get("glob"), "limit": args.get("limit")}
    )


def ws_read_file(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _fs(
        ws, {"op": "read", "path": args["path"], "start": args.get("start_line"), "lines": args.get("lines")}
    )


def ws_search(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _fs(
        ws,
        {
            "op": "search",
            "query": args["query"],
            "regex": args.get("regex") or None,
            "path": args.get("path"),
            "glob": args.get("glob"),
            "limit": args.get("limit"),
        },
    )


def ws_write_file(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    req = {"op": "write", "path": args["path"], "content": args["content"]}
    out = _ws(ws)("fs", req)  # empty content is allowed
    if out.get("error"):
        raise ToolError(out["error"])
    return out


def ws_edit_file(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    req = {"op": "edit", "path": args["path"], "old": args["old_text"], "new": args["new_text"]}
    out = _ws(ws)("fs", req)  # new_text may be empty (a deletion)
    if out.get("error"):
        raise ToolError(out["error"])
    return out


def ws_delete_file(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _fs(ws, {"op": "delete", "path": args["path"]})


def ws_run_command(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    body: dict[str, Any] = {"command": args["command"]}
    if args.get("timeout_s"):
        body["timeoutS"] = args["timeout_s"]
    return _ws(ws)("command", body)


def ws_git_diff(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    return _ws(ws)("diff", {})


IMPLEMENTATIONS = {
    "ws_list_files": ws_list_files,
    "ws_read_file": ws_read_file,
    "ws_search": ws_search,
    "ws_write_file": ws_write_file,
    "ws_edit_file": ws_edit_file,
    "ws_delete_file": ws_delete_file,
    "ws_run_command": ws_run_command,
    "ws_git_diff": ws_git_diff,
}
