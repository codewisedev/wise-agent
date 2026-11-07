from __future__ import annotations

import os

import pytest

from wise_runtime.tools import ToolRegistry
from wise_runtime.tools.workspace import Workspace

from .conftest import PRODUCT_TOOLS


@pytest.fixture
def registry(repo_tree):
    return ToolRegistry(PRODUCT_TOOLS, Workspace(repo_tree["project"], (repo_tree["root"],)))


def ok(registry, name, args):
    out, err, _ = registry.execute(name, args)
    assert err is None, err
    return out


def err(registry, name, args):
    out, error, _ = registry.execute(name, args)
    assert out is None and error
    return error


def test_project_context_marks_unreadable_repos(registry):
    ctx = ok(registry, "get_project_context", {})
    repos = {r["name"]: r for r in ctx["repositories"]}
    assert repos["api"]["readable"] is True
    assert repos["web"]["readable"] is False and "remote" in repos["web"]["unavailable"]


def test_list_files_skips_noise(registry):
    entries = ok(registry, "list_files", {"repository": "api"})["entries"]
    assert "src/booking.go" in entries and "README.md" in entries
    assert not any(e.startswith((".git", "node_modules")) for e in entries)


def test_read_file_pages_and_numbers_lines(registry):
    out = ok(registry, "read_file", {"repository": "api", "path": "README.md", "max_lines": 2})
    assert out["content"].splitlines()[0].strip().startswith("1") and "# API" in out["content"]
    assert out["truncated"] is True and out["totalLines"] > 1000
    page = ok(
        registry, "read_file", {"repository": "api", "path": "README.md", "start_line": 3, "max_lines": 1}
    )
    assert page["startLine"] == 3 and page["endLine"] == 3


@pytest.mark.parametrize(
    ("path", "fragment"),
    [
        ("../outside/passwords.txt", "escapes"),
        ("escape/passwords.txt", "escapes"),
        ("escape.txt", "escapes"),
        ("/etc/passwd", "relative"),
        (".git/secret", ".git"),
        ("bin.dat", "binary"),
        ("missing.txt", "not found"),
    ],
)
def test_read_file_refuses_unsafe_paths(registry, path, fragment):
    assert fragment in err(registry, "read_file", {"repository": "api", "path": path})


def test_repository_and_argument_errors(registry):
    assert "unavailable" in err(registry, "read_file", {"repository": "web", "path": "x"})
    assert "unknown repository" in err(registry, "read_file", {"repository": "nope", "path": "x"})
    assert "invalid arguments" in err(registry, "read_file", {"repository": "api"})
    assert "unknown tool" in err(registry, "rm_rf", {})


def test_search_code(registry):
    out = ok(registry, "search_code", {"pattern": "func CreateBooking"})
    assert out["matches"] == [
        {"repository": "api", "path": "src/booking.go", "line": 3, "text": "func CreateBooking() {}"}
    ]
    assert ok(registry, "search_code", {"pattern": "hunter2"})["matches"] == []  # symlinks not followed
    assert (
        ok(registry, "search_code", {"pattern": "booking", "glob": "*.md"})["matches"][0]["path"]
        == "README.md"
    )
    assert "invalid search" in err(registry, "search_code", {"pattern": "("})


def test_repos_outside_allowed_roots_are_unavailable(repo_tree, tmp_path):
    ws = Workspace(repo_tree["project"], (str(tmp_path / "elsewhere"),))
    assert "api" not in ws.repos and "allowed" in ws.unavailable["api"]


def test_no_project(repo_tree):
    reg = ToolRegistry(PRODUCT_TOOLS, Workspace(None))
    assert reg.execute("get_project_context", {})[0]["project"] is None
    assert "no project" in reg.execute("list_files", {"repository": "api"})[1]


def test_write_tools_are_refused(repo_tree):
    with pytest.raises(ValueError, match="side effects"):
        ToolRegistry([{**PRODUCT_TOOLS[0], "sideEffects": "write"}], Workspace(None))
    assert os.path.exists(repo_tree["outside"])  # nothing touched


def test_numeric_arguments_are_clamped_even_without_schema_bounds(repo_tree):
    loose = [{**t, "inputSchema": {"type": "object"}} for t in PRODUCT_TOOLS]
    reg = ToolRegistry(loose, Workspace(repo_tree["project"], (repo_tree["root"],)))
    out, err, _ = reg.execute("read_file", {"repository": "api", "path": "README.md", "max_lines": -5})
    assert err is None and out["endLine"] == 1  # clamped to 1 line
    _, err, _ = reg.execute("read_file", {"repository": "api", "path": "README.md", "start_line": "abc"})
    assert err == "start_line must be an integer"
