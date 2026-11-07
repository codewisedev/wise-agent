"""Read-only repository tools: get_project_context, list_files, read_file, search_code."""

from __future__ import annotations

import json
import os
import subprocess
from typing import Any

from .base import ToolError
from .workspace import Workspace

SKIP_DIRS = {
    ".git",
    "node_modules",
    "dist",
    "build",
    ".venv",
    "venv",
    "__pycache__",
    "target",
    ".next",
    "vendor",
}
MAX_LIST_ENTRIES = 400
MAX_READ_BYTES = 200_000
DEFAULT_MAX_LINES = 400


def _int_arg(args: dict[str, Any], key: str, default: int, hi: int) -> int:
    """Coerce and clamp an integer argument (defence in depth beyond the tool's input schema)."""
    raw = args.get(key)
    if raw is None or raw == "":
        return default
    if isinstance(raw, bool):
        raise ToolError(f"{key} must be an integer")
    try:
        return min(max(int(raw), 1), hi)
    except (TypeError, ValueError) as e:
        raise ToolError(f"{key} must be an integer") from e


def get_project_context(ws: Workspace, _args: dict[str, Any]) -> dict[str, Any]:
    if not ws.project:
        return {"project": None, "note": "No project is attached; repository tools are unavailable."}
    p = ws.project
    return {
        "project": {"name": p["name"], "slug": p["slug"], "description": p.get("description", "")},
        "repositories": [
            {
                "name": r["name"],
                "role": r.get("role"),
                "sourceType": r.get("sourceType"),
                "branch": r.get("currentBranch"),
                "headSha": r.get("headSha"),
                "readable": r["name"] in ws.repos,
                **({"unavailable": ws.unavailable[r["name"]]} if r["name"] in ws.unavailable else {}),
            }
            for r in p.get("repositories", [])
        ],
    }


def list_files(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    repo = ws.repo(args.get("repository"))
    start = ws.resolve(repo, args.get("path"))
    if not os.path.isdir(start):
        raise ToolError("path is not a directory")
    max_depth = _int_arg(args, "max_depth", 2, hi=4)
    entries: list[str] = []
    truncated = False
    base_depth = start.rstrip(os.sep).count(os.sep)
    for dirpath, dirnames, filenames in os.walk(start):
        dirnames[:] = sorted(
            d for d in dirnames if d not in SKIP_DIRS and not os.path.islink(os.path.join(dirpath, d))
        )
        depth = dirpath.rstrip(os.sep).count(os.sep) - base_depth
        if depth >= max_depth:
            dirnames[:] = []
        rel_dir = os.path.relpath(dirpath, repo.root)
        for d in dirnames:
            entries.append(os.path.normpath(os.path.join(rel_dir, d)) + "/")
        for f in sorted(filenames):
            entries.append(os.path.normpath(os.path.join(rel_dir, f)))
        if len(entries) >= MAX_LIST_ENTRIES:
            truncated = True
            break
    return {"repository": repo.name, "entries": sorted(entries[:MAX_LIST_ENTRIES]), "truncated": truncated}


def read_file(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    repo = ws.repo(args.get("repository"))
    path = ws.resolve(repo, args["path"])
    if not os.path.isfile(path):
        raise ToolError("file not found")
    with open(path, "rb") as fh:
        raw = fh.read(MAX_READ_BYTES + 1)
    if b"\x00" in raw[:8000]:
        raise ToolError("binary file")
    text = raw[:MAX_READ_BYTES].decode("utf-8", errors="replace")
    lines = text.splitlines()
    start = _int_arg(args, "start_line", 1, hi=10_000_000)
    count = _int_arg(args, "max_lines", DEFAULT_MAX_LINES, hi=2000)
    chunk = lines[start - 1 : start - 1 + count]
    return {
        "repository": repo.name,
        "path": os.path.relpath(path, repo.root),
        "startLine": start,
        "endLine": start + len(chunk) - 1,
        "totalLines": len(lines),
        "truncated": len(raw) > MAX_READ_BYTES or start - 1 + count < len(lines),
        "content": "\n".join(f"{start + i:>5}  {line}" for i, line in enumerate(chunk)),
    }


def search_code(ws: Workspace, args: dict[str, Any]) -> dict[str, Any]:
    if not ws.project:
        raise ToolError("no project is attached to this run")
    repos = [ws.repo(args["repository"])] if args.get("repository") else list(ws.repos.values())
    if not repos:
        raise ToolError("no readable repositories in this project")
    max_results = _int_arg(args, "max_results", 50, hi=200)
    matches: list[dict[str, Any]] = []
    for repo in repos:
        cmd = [
            "rg",
            "--json",
            "--smart-case",
            "--max-count",
            "20",
            "--max-filesize",
            "1M",
            "--max-columns",
            "300",
            "--glob",
            "!.git",
        ]
        if args.get("glob"):
            cmd += ["--glob", args["glob"]]
        cmd += ["--regexp", args["pattern"], "--", "."]
        try:
            proc = subprocess.run(cmd, cwd=repo.root, capture_output=True, text=True, timeout=20, check=False)
        except FileNotFoundError as e:
            raise ToolError("ripgrep (rg) is not installed in the runtime") from e
        except subprocess.TimeoutExpired as e:
            raise ToolError("search timed out; narrow the pattern or add a glob") from e
        if proc.returncode == 2 and not proc.stdout:
            raise ToolError(f"invalid search: {proc.stderr.strip()[:300]}")
        for line in proc.stdout.splitlines():
            ev = json.loads(line)
            if ev.get("type") != "match":
                continue
            d = ev["data"]
            matches.append(
                {
                    "repository": repo.name,
                    "path": os.path.normpath(d["path"].get("text", "")),
                    "line": d["line_number"],
                    "text": d["lines"].get("text", "").rstrip("\n")[:300],
                }
            )
            if len(matches) >= max_results:
                return {"matches": matches, "truncated": True}
    return {"matches": matches, "truncated": False}


IMPLEMENTATIONS = {
    "get_project_context": get_project_context,
    "list_files": list_files,
    "read_file": read_file,
    "search_code": search_code,
}
