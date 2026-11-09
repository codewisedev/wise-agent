from __future__ import annotations

from pathlib import Path

import pytest

from wise_runtime.client import ApiError
from wise_runtime.indexer.content import dependencies, doc_chunks, git_head, looks_secret
from wise_runtime.indexer.indexer import Settings, index_repository, walk
from wise_runtime.indexer.symbols import parse
from wise_runtime.tools.registry import ToolRegistry
from wise_runtime.tools.workspace import Workspace

FAKE_AWS = "AKIA" + "Z7Q2LX9VP4NM7BK1"


def write(root: Path, rel: str, text: str | bytes) -> None:
    p = root / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(text, bytes):
        p.write_bytes(text)
    else:
        p.write_text(text)


@pytest.fixture
def repo(tmp_path: Path) -> Path:
    write(tmp_path, ".gitignore", "generated/\n*.log\n")
    write(
        tmp_path,
        "api/gateway.go",
        'package api\n\nimport "context"\n\n// Gateway routes calls.\ntype Gateway struct{}\n\n'
        "// Reconfigure swaps the configuration.\n"
        "func (g *Gateway) Reconfigure(ctx context.Context) error {\n\treturn nil\n}\n",
    )
    write(
        tmp_path,
        "web/Board.tsx",
        'import { useState } from "react";\n/** A board */\nexport function Board() {\n  return null;\n}\n'
        "export interface Card { id: string }\n",
    )
    write(
        tmp_path,
        "web/Card.vue",
        '<template><div/></template>\n<script setup lang="ts">\nfunction openCard() {}\n</script>\n',
    )
    write(
        tmp_path,
        "tools/sync.py",
        'import os\n\n\nclass Syncer:\n    """Syncs things."""\n\n    def run(self):\n        pass\n',
    )
    write(
        tmp_path,
        "core/src/lib.rs",
        "use std::io;\npub struct Index;\nimpl Index {\n    pub fn build(&self) {}\n}\n",
    )
    write(tmp_path, "docs/guide.md", "# Guide\n\nIntro.\n\n## Install\n\nRun make.\n\n## Usage\n\nCall it.\n")
    write(
        tmp_path,
        "package.json",
        '{"dependencies": {"react": "^19.0.0"}, "devDependencies": {"vitest": "^3"}}',
    )
    write(
        tmp_path,
        "go.mod",
        "module x\n\ngo 1.24\n\nrequire (\n"
        "\tgithub.com/google/uuid v1.6.0\n\tgolang.org/x/text v0.1.0 // indirect\n)\n",
    )
    write(
        tmp_path,
        "tools/pyproject.toml",
        '[project]\nname="t"\ndependencies=["httpx>=0.28"]\n[dependency-groups]\ndev=["pytest"]\n',
    )
    write(
        tmp_path,
        "core/Cargo.toml",
        '[dependencies]\nserde = "1"\n[dev-dependencies]\ntokio = { version = "1" }\n',
    )
    write(tmp_path, "config/app.yaml", "port: 8080\n")
    write(tmp_path, ".env", "TOKEN=abc\n")
    write(tmp_path, "deploy/aws.ts", f'export const key = "{FAKE_AWS}";\n')
    write(tmp_path, "generated/big.go", "package g\n")
    write(tmp_path, "debug.log", "noise\n")
    write(tmp_path, "assets/logo.png", b"\x89PNG\x00\x00binary")
    write(tmp_path, "node_modules/lib/index.js", "module.exports = 1\n")
    return tmp_path


def run(
    root: Path, manifest: dict[str, str] | None = None, **kw: object
) -> tuple[dict[str, dict], list[str], object]:
    files: dict[str, dict] = {}
    deleted: list[str] = []

    def upload(batch: list[dict], gone: list[str]) -> None:
        for f in batch:
            files[f["path"]] = f
        deleted.extend(gone)

    settings = Settings(exclude=["node_modules/"], docs=["*.md"], **kw)  # type: ignore[arg-type]
    res = index_repository(str(root), settings, manifest or {}, upload)
    return files, deleted, res


def test_symbols_across_languages(repo: Path) -> None:
    files, _, _ = run(repo)
    names = {(f["path"], s["kind"], s["name"], s["parent"]) for f in files.values() for s in f["symbols"]}
    assert ("api/gateway.go", "method", "Reconfigure", "Gateway") in names
    assert ("api/gateway.go", "struct", "Gateway", "") in names
    assert ("web/Board.tsx", "component", "Board", "") in names
    assert ("web/Board.tsx", "interface", "Card", "") in names
    assert ("web/Card.vue", "function", "openCard", "") in names
    assert ("tools/sync.py", "method", "run", "Syncer") in names
    assert ("core/src/lib.rs", "method", "build", "Index") in names
    go = files["api/gateway.go"]
    rec = next(s for s in go["symbols"] if s["name"] == "Reconfigure")
    assert rec["doc"] == "Reconfigure swaps the configuration." and rec["exported"] and rec["startLine"] == 9
    assert go["imports"] == ["context"]
    vue = next(s for s in files["web/Card.vue"]["symbols"] if s["name"] == "openCard")
    assert vue["startLine"] == 3  # line numbers of the .vue file, not the script block


def test_chunks_dependencies_and_skips(repo: Path) -> None:
    files, _, res = run(repo)
    headings = [c["heading"] for c in files["api/gateway.go"]["chunks"]]
    assert "method Gateway.Reconfigure" in headings
    doc = files["docs/guide.md"]["chunks"]
    assert [c["heading"] for c in doc] == ["Guide", "Guide > Install", "Guide > Usage"] and doc[0][
        "kind"
    ] == "doc"
    assert files["config/app.yaml"]["chunks"][0]["kind"] == "config"
    assert files["deploy/aws.ts"]["skippedReason"] == "secret" and not files["deploy/aws.ts"]["chunks"]
    assert ".env" not in files  # ignored by default name rules only if walked; never indexed with content
    assert "generated/big.go" not in files and "debug.log" not in files  # .gitignore
    assert not any(p.startswith("node_modules/") for p in files)  # configured exclude
    assert "assets/logo.png" not in files  # not a text candidate
    deps = {(d["ecosystem"], d["name"], d["dev"]) for d in res.dependencies}  # type: ignore[attr-defined]
    assert {
        ("npm", "react", False),
        ("npm", "vitest", True),
        ("go", "github.com/google/uuid", False),
        ("pypi", "httpx", False),
        ("pypi", "pytest", True),
        ("cargo", "serde", False),
        ("cargo", "tokio", True),
    } <= deps
    assert ("go", "golang.org/x/text", False) not in deps  # indirect


def test_env_files_are_skipped_as_secrets(repo: Path) -> None:
    files, _, _ = run(repo)
    if ".env" in files:
        assert files[".env"]["skippedReason"] == "secret" and not files[".env"]["chunks"]


def test_size_cap_and_binary(repo: Path) -> None:
    write(repo, "data/huge.sql", "select 1;\n" * 400)
    write(repo, "data/bin.ts", b"const a = 1\x00\x00")
    files, _, _ = run(repo, max_file_kb=1)
    assert files["data/huge.sql"]["skippedReason"] == "too_large"
    assert files["data/bin.ts"]["skippedReason"] == "binary"


def test_incremental_upload_and_deletions(repo: Path) -> None:
    files, _, _ = run(repo)
    manifest = {p: f["contentHash"] for p, f in files.items()}
    (repo / "web/Card.vue").unlink()
    write(repo, "api/gateway.go", "package api\n\nfunc New() {}\n")
    again, deleted, res = run(repo, manifest)
    assert set(again) == {"api/gateway.go"}
    assert deleted == ["web/Card.vue"]
    assert res.unchanged > 5  # type: ignore[attr-defined]


def test_truncated_walk_never_deletes(repo: Path) -> None:
    files, _, _ = run(repo)
    manifest = {p: f["contentHash"] for p, f in files.items()}
    _, deleted, res = run(repo, manifest, max_files=2)
    assert res.truncated and deleted == []  # type: ignore[attr-defined]


def test_walk_respects_nested_gitignore(repo: Path) -> None:
    write(repo, "web/.gitignore", "*.snap\n")
    write(repo, "web/a.snap", "x")
    paths, _ = walk(str(repo), Settings(exclude=["node_modules/"]))
    assert "web/a.snap" not in paths and "web/Board.tsx" in paths


def test_git_head_with_packed_refs(tmp_path: Path) -> None:
    write(tmp_path, ".git/HEAD", "ref: refs/heads/main\n")
    write(tmp_path, ".git/packed-refs", "# pack-refs\nabc123 refs/heads/main\n")
    assert git_head(str(tmp_path)) == "abc123"
    write(tmp_path, ".git/refs/heads/main", "def456\n")
    assert git_head(str(tmp_path)) == "def456"
    assert git_head(str(tmp_path / "nope")) == ""


def test_secret_guard_spares_placeholders() -> None:
    assert not looks_secret("t.go", '"secret": "sk-deepseek-test-1"', [])
    assert looks_secret("c.py", 'API_KEY = "q8Zr2Lx9Vp4Nm7Bk1Cj6Hy3We5"', [])
    assert looks_secret("x.txt", "-----BEGIN OPENSSH PRIVATE KEY-----", [])
    import re

    assert looks_secret("a.txt", "corp-internal-42", [re.compile(r"corp-internal-\d+")])


def test_doc_chunks_ignore_headings_in_code_fences() -> None:
    chunks = doc_chunks("# A\n```\n# not a heading\n```\n## B\ntext\n", 60)
    assert [c["heading"] for c in chunks] == ["A", "A > B"]


def test_dependencies_tolerate_bad_manifests() -> None:
    assert dependencies("package.json", "{not json") == []
    assert dependencies("Cargo.toml", "[[[") == []


def test_parse_unknown_language_is_empty() -> None:
    assert parse("notes.txt", b"hello").symbols == []


CONTEXT_TOOL_DEFS = [
    {
        "name": "search_context",
        "sideEffects": "read",
        "inputSchema": {"type": "object", "required": ["query"], "properties": {"query": {"type": "string"}}},
    },
]


def test_context_tools_call_the_control_plane() -> None:
    calls: list[tuple[str, dict]] = []

    def remote(kind: str, params: dict) -> dict:
        calls.append((kind, params))
        return {"items": [{"path": "api/gateway.go"}]}

    ws = Workspace({"name": "p", "repositories": []}, (), context=remote)
    out, err, _ = ToolRegistry(CONTEXT_TOOL_DEFS, ws).execute("search_context", {"query": "gateway"})
    assert err is None and out == {"items": [{"path": "api/gateway.go"}]}
    assert calls == [("search", {"q": "gateway"})]


def test_context_tool_errors_go_back_to_the_model() -> None:
    def remote(kind: str, params: dict) -> dict:
        raise ApiError(409, "this run has no project attached")

    ws = Workspace({"name": "p", "repositories": []}, (), context=remote)
    out, err, _ = ToolRegistry(CONTEXT_TOOL_DEFS, ws).execute("search_context", {"query": "x"})
    assert out is None and err is not None and "409" in err


def test_context_tools_need_a_project() -> None:
    ws = Workspace(None, (), context=lambda k, p: {})
    _, err, _ = ToolRegistry(CONTEXT_TOOL_DEFS, ws).execute("search_context", {"query": "x"})
    assert err is not None and "no project" in err


def test_settings_change_reindexes_unchanged_files(repo: Path) -> None:
    files, _, _ = run(repo)
    manifest = {p: f["contentHash"] for p, f in files.items()}
    again, _, _ = run(repo, manifest)
    assert again == {}  # nothing changed
    # A new secret pattern must purge files indexed before it existed.
    files2, _, _ = run(repo, manifest, secret_patterns=[r"Gateway routes"])
    assert files2["api/gateway.go"]["skippedReason"] == "secret"
    assert len(files2) > 5  # every file re-processed under the new settings


def test_batches_stay_under_server_limits(tmp_path: Path) -> None:
    for i in range(4):
        write(tmp_path, f"big{i}.txt", "\n".join(f"line {n}" for n in range(15000)))
    sizes: list[int] = []
    settings = Settings(chunk_lines=10, max_file_kb=4096)
    index_repository(str(tmp_path), settings, {}, lambda b, d: sizes.append(sum(len(f["chunks"]) for f in b)))
    assert sizes and max(sizes) <= 5000


def test_search_memory_tool_calls_the_memory_endpoint() -> None:
    calls: list[tuple[str, dict]] = []

    def remote(kind: str, params: dict) -> dict:
        calls.append((kind, params))
        return {"items": []}

    defs = [
        {
            "name": "search_memory",
            "sideEffects": "none",
            "inputSchema": {
                "type": "object",
                "required": ["query"],
                "properties": {"query": {"type": "string"}},
            },
        }
    ]
    ws = Workspace({"name": "p", "repositories": []}, (), context=remote)
    _, err, _ = ToolRegistry(defs, ws).execute("search_memory", {"query": "retries"})
    assert err is None and calls == [("memory", {"q": "retries"})]
