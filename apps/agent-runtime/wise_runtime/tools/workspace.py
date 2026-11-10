"""Read-only, path-confined access to a project's local repositories."""

from __future__ import annotations

import os
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from .base import ToolError


@dataclass(frozen=True)
class Repo:
    name: str
    root: str  # resolved real path
    role: str
    branch: str | None
    head_sha: str | None


def _within(path: str, root: str) -> bool:
    return path == root or path.startswith(root.rstrip(os.sep) + os.sep)


class Workspace:
    """Maps repository names to roots and resolves paths safely inside them.

    Every path is resolved with realpath, so `..` segments and symlinks that
    point outside the repository are rejected.
    """

    def __init__(
        self,
        project: dict[str, Any] | None,
        allowed_roots: tuple[str, ...] = (),
        context: Callable[[str, dict[str, Any]], dict[str, Any]] | None = None,
        task_workspace: Callable[[str, dict[str, Any]], dict[str, Any]] | None = None,
    ):
        self.project = project
        # The run's task workspace (its sandbox), through the control plane;
        # None when the run has no workspace.
        self.task_workspace = task_workspace
        # Index queries go through the control plane (scoped to the run's project).
        self.context = context
        self.repos: dict[str, Repo] = {}
        self.unavailable: dict[str, str] = {}
        for r in (project or {}).get("repositories", []):
            local = r.get("localPath")
            if r.get("sourceType") != "local" or not local:
                self.unavailable[r["name"]] = "remote repository (not checked out locally)"
                continue
            root = os.path.realpath(local)
            if allowed_roots and not any(_within(root, a) for a in allowed_roots):
                self.unavailable[r["name"]] = "outside the runtime's allowed repository roots"
                continue
            if not os.path.isdir(root):
                self.unavailable[r["name"]] = "path not mounted in the runtime"
                continue
            self.repos[r["name"]] = Repo(
                r["name"], root, r.get("role", ""), r.get("currentBranch"), r.get("headSha")
            )

    def repo(self, name: str | None) -> Repo:
        if not self.project:
            raise ToolError("no project is attached to this run")
        if name is None:
            if len(self.repos) == 1:
                return next(iter(self.repos.values()))
            raise ToolError(f"specify a repository: one of {sorted(self.repos)}")
        if name in self.unavailable:
            raise ToolError(f"repository {name!r} is unavailable: {self.unavailable[name]}")
        if name not in self.repos:
            raise ToolError(f"unknown repository {name!r}; available: {sorted(self.repos)}")
        return self.repos[name]

    def resolve(self, repo: Repo, rel: str | None) -> str:
        rel = (rel or "").strip()
        if os.path.isabs(rel):
            raise ToolError("paths must be relative to the repository root")
        full = os.path.realpath(os.path.join(repo.root, rel))
        if not _within(full, repo.root):
            raise ToolError("path escapes the repository")
        parts = os.path.relpath(full, repo.root).split(os.sep)
        if ".git" in parts:
            raise ToolError("the .git directory is not accessible")
        return full
