from __future__ import annotations

import json
import os
import subprocess
from typing import Any

import pytest

from wise_runtime.client import ApiError

PRODUCT_TOOLS = [
    {
        "name": "get_project_context",
        "description": "ctx",
        "sideEffects": "none",
        "permissionsRequired": [],
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
    {
        "name": "list_files",
        "description": "ls",
        "sideEffects": "read",
        "permissionsRequired": [],
        "inputSchema": {
            "type": "object",
            "required": ["repository"],
            "additionalProperties": False,
            "properties": {
                "repository": {"type": "string"},
                "path": {"type": "string"},
                "max_depth": {"type": "integer", "minimum": 1, "maximum": 4},
            },
        },
    },
    {
        "name": "read_file",
        "description": "cat",
        "sideEffects": "read",
        "permissionsRequired": [],
        "inputSchema": {
            "type": "object",
            "required": ["repository", "path"],
            "additionalProperties": False,
            "properties": {
                "repository": {"type": "string"},
                "path": {"type": "string"},
                "start_line": {"type": "integer"},
                "max_lines": {"type": "integer"},
            },
        },
    },
    {
        "name": "search_code",
        "description": "rg",
        "sideEffects": "read",
        "permissionsRequired": [],
        "inputSchema": {
            "type": "object",
            "required": ["pattern"],
            "additionalProperties": False,
            "properties": {
                "pattern": {"type": "string"},
                "repository": {"type": "string"},
                "glob": {"type": "string"},
                "max_results": {"type": "integer"},
            },
        },
    },
]

OUTPUT_SCHEMA = {
    "type": "object",
    "required": ["summary", "features"],
    "additionalProperties": False,
    "properties": {"summary": {"type": "string"}, "features": {"type": "array", "items": {"type": "string"}}},
}

DEFINITION = {
    "name": "product-agent",
    "version": "1.0.0",
    "output_schema": OUTPUT_SCHEMA,
    "execution_limits": {"max_turns": 6, "max_tool_calls": 3, "timeout_s": 60},
    "error_handling": {"max_attempts": 2, "invalid_output_retries": 1},
}


def final(output: dict[str, Any]) -> dict[str, Any]:
    return {"action": "final", "final": output, "thought": "done"}


def tools(*calls: tuple[str, dict[str, Any]]) -> dict[str, Any]:
    return {
        "action": "call_tools",
        "toolCalls": [{"tool": t, "arguments": a} for t, a in calls],
        "thought": "look",
    }


class FakeControlPlane:
    """In-memory stand-in for ControlPlane with scripted model turns."""

    def __init__(
        self,
        turns: list[dict[str, Any]],
        *,
        project: dict[str, Any] | None = None,
        cancel_after: int | None = None,
    ):
        self.turns = list(turns)
        self.project = project
        self.cancel_after = cancel_after
        self.transcripts: list[list[dict[str, str]]] = []
        self.events: list[dict[str, Any]] = []
        self.completed: dict[str, Any] | None = None
        self.failed: dict[str, Any] | None = None
        self.claimed = False
        self.heartbeats = 0
        self.reject_output = False

    def claim(self, runtime_id: str) -> dict[str, Any] | None:
        if self.claimed:
            return None
        self.claimed = True
        return {
            "run": {
                "id": "run-1",
                "agentName": "product-agent",
                "attempt": 1,
                "input": {"request": "build it"},
            },
            "runToken": "wrt_x",
            "leaseSeconds": 3,
            "definition": DEFINITION,
            "prompt": "You are a test agent.",
            "tools": PRODUCT_TOOLS,
            "project": self.project,
        }

    def heartbeat(self, run_id: str, token: str) -> dict[str, Any]:
        self.heartbeats += 1
        return {"cancelRequested": False, "leaseSeconds": 3}

    def append_events(self, run_id: str, token: str, events: list[dict[str, Any]]) -> None:
        self.events.extend(events)

    def model_turn(self, run_id: str, token: str, messages: list[dict[str, str]]) -> dict[str, Any]:
        self.transcripts.append([dict(m) for m in messages])
        if self.cancel_after is not None and len(self.transcripts) > self.cancel_after:
            raise AssertionError("model called after cancellation")
        if not self.turns:
            raise AssertionError("no scripted turn left")
        return self.turns.pop(0)

    def complete(self, run_id: str, token: str, output: dict[str, Any]) -> None:
        if self.reject_output:
            raise ApiError(422, "output does not match the agent's output_schema")
        self.completed = output

    def fail(
        self, run_id: str, token: str, error: str, *, retryable: bool = False, cancelled: bool = False
    ) -> None:
        self.failed = {"error": error, "retryable": retryable, "cancelled": cancelled}

    def event_types(self) -> list[str]:
        return [e["type"] for e in self.events]


def git(cwd: str, *args: str) -> None:
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "t",
        "GIT_AUTHOR_EMAIL": "t@t",
        "GIT_COMMITTER_NAME": "t",
        "GIT_COMMITTER_EMAIL": "t@t",
        "GIT_CONFIG_GLOBAL": "/dev/null",
    }
    subprocess.run(["git", *args], cwd=cwd, env=env, check=True, capture_output=True)


@pytest.fixture
def repo_tree(tmp_path):
    """A fake allowed root with one repository, plus a secret outside it."""
    root = tmp_path / "repos"
    repo = root / "api"
    (repo / "src").mkdir(parents=True)
    (repo / "src" / "booking.go").write_text("package booking\n\nfunc CreateBooking() {}\n")
    (repo / "README.md").write_text("# API\nBookings service.\n" + "line\n" * 1000)
    (repo / "node_modules" / "x").mkdir(parents=True)
    (repo / "bin.dat").write_bytes(b"\x00\x01\x02")
    git(str(repo), "init", "-q")
    (repo / ".git" / "secret").write_text("do not read")
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "passwords.txt").write_text("hunter2")
    (repo / "escape").symlink_to(outside)
    (repo / "escape.txt").symlink_to(outside / "passwords.txt")
    project = {
        "id": "p1",
        "name": "Salon",
        "slug": "salon",
        "description": "",
        "repositories": [
            {
                "id": "r1",
                "name": "api",
                "role": "backend",
                "sourceType": "local",
                "localPath": str(repo),
                "currentBranch": "main",
                "headSha": "abc",
            },
            {
                "id": "r2",
                "name": "web",
                "role": "frontend",
                "sourceType": "remote",
                "remoteUrl": "git@x:y.git",
            },
        ],
    }
    return {"root": str(root), "repo": str(repo), "outside": str(outside), "project": project}


def dumps(v: Any) -> str:
    return json.dumps(v)
