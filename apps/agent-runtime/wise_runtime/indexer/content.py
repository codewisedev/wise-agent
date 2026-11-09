"""Chunking, dependency manifests, the secret guard and git HEAD (no git binary needed)."""

from __future__ import annotations

import json
import math
import os
import re
import tomllib
from collections import Counter
from typing import Any

from .symbols import Symbol

MAX_CHUNK_BYTES = 5500

# ------------------------------------------------------------------ secrets

SECRET_NAME = re.compile(
    r"(?i)(^|/)(\.env(\..*)?|\.envrc|id_(rsa|dsa|ecdsa|ed25519)|.*\.(pem|key|p12|pfx|jks|keystore)|"
    r"credentials(\.json)?|\.netrc|\.npmrc|\.pypirc|secrets?\.(ya?ml|json|toml))$"
)
SECRET_CONTENT = [
    re.compile(r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY-----"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),  # AWS access key
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}\b"),  # GitHub
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{40,}\b"),
    re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}\b"),  # Slack
    re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b"),  # Google API key
    re.compile(r"\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{24,}\b"),  # OpenAI / Anthropic
    re.compile(r"\bpk_\d+_[A-Z0-9]{20,}\b"),  # ClickUp
    re.compile(r"\bATATT[A-Za-z0-9_=-]{20,}\b"),  # Atlassian
    re.compile(r"\bglpat-[A-Za-z0-9_-]{20,}\b"),  # GitLab
]
ASSIGNMENT = re.compile(
    r"(?i)\b(api[_-]?key|secret|token|passw(or)?d|private[_-]?key|client[_-]?secret)\b[\"']?\s*[:=]\s*[\"']([^\"'\s]{16,})[\"']"
)


def _entropy(s: str) -> float:
    counts = Counter(s)
    return -sum(c / len(s) * math.log2(c / len(s)) for c in counts.values())


_WORD = re.compile(r"^[a-z]{3,}$")


def _placeholder(value: str) -> bool:
    """Readable test values ("sk-deepseek-test-1", "atlassian-token-123456") are not secrets:
    real keys are random, not two or more plain words."""
    return sum(1 for seg in re.split(r"[-_.]", value) if _WORD.match(seg)) >= 2


def looks_secret(path: str, text: str, extra: list[re.Pattern[str]]) -> bool:
    if SECRET_NAME.search(path):
        return True
    for rx in extra:  # user patterns always win
        if rx.search(text):
            return True
    for rx in SECRET_CONTENT:
        for m in rx.finditer(text):
            if m.group(0).startswith("-----BEGIN") or not _placeholder(m.group(0)):
                return True
    for m in ASSIGNMENT.finditer(text):
        value = m.group(3)
        if value.startswith(("${", "{{", "process.env", "os.environ")) or _placeholder(value):
            continue
        if _entropy(value) >= 3.5:
            return True
    return False


# ------------------------------------------------------------------ chunks


def _clip(lines: list[str]) -> str:
    text = "\n".join(lines)
    if len(text.encode()) <= MAX_CHUNK_BYTES:
        return text
    return text.encode()[:MAX_CHUNK_BYTES].decode("utf-8", "ignore")


def _windows(
    lines: list[str], start: int, end: int, size: int, heading: str, kind: str
) -> list[dict[str, Any]]:
    """Chunks of `size` lines over [start, end] (1-based, inclusive); blank-only windows dropped."""
    out = []
    parts = max(1, math.ceil((end - start + 1) / size))
    for i in range(parts):
        a = start + i * size
        b = min(end, a + size - 1)
        body = lines[a - 1 : b]
        if not any(line.strip() for line in body):
            continue
        h = heading if parts == 1 or not heading else f"{heading} (part {i + 1}/{parts})"
        out.append({"kind": kind, "startLine": a, "endLine": b, "heading": h, "content": _clip(body)})
    return out


def code_chunks(text: str, symbols: list[Symbol], size: int) -> list[dict[str, Any]]:
    """One chunk per outermost definition (split when long), plus the code between them."""
    lines = text.split("\n")
    n = len(lines)
    outer: list[Symbol] = []
    for s in symbols:  # sorted by start, longest first
        if outer and s.start_line <= outer[-1].end_line:
            continue
        outer.append(s)
    chunks: list[dict[str, Any]] = []
    cursor = 1
    for s in outer:
        if s.start_line > cursor:
            chunks += _windows(lines, cursor, s.start_line - 1, size, "", "code")
        title = f"{s.parent}.{s.name}" if s.parent else s.name
        chunks += _windows(lines, s.start_line, min(s.end_line, n), size, f"{s.kind} {title}", "code")
        cursor = s.end_line + 1
    if cursor <= n:
        chunks += _windows(lines, cursor, n, size, "", "code")
    return chunks


_HEADING = re.compile(r"^(#{1,6})\s+(.+?)\s*#*\s*$")


def doc_chunks(text: str, size: int) -> list[dict[str, Any]]:
    """Markdown-style docs split by heading, keeping the heading path as context."""
    lines = text.split("\n")
    sections: list[tuple[int, str]] = []  # (start line, heading path)
    path: list[str] = []
    in_fence = False
    for i, line in enumerate(lines, 1):
        if line.lstrip().startswith(("```", "~~~")):
            in_fence = not in_fence
        m = None if in_fence else _HEADING.match(line)
        if m:
            level = len(m.group(1))
            path = [*path[: level - 1], m.group(2)]
            sections.append((i, " > ".join(path)))
    if not sections or sections[0][0] > 1:
        sections.insert(0, (1, ""))
    out: list[dict[str, Any]] = []
    for idx, (start, heading) in enumerate(sections):
        end = sections[idx + 1][0] - 1 if idx + 1 < len(sections) else len(lines)
        out += _windows(lines, start, end, size * 2, heading, "doc")
    return out


def config_chunks(text: str, path: str, size: int) -> list[dict[str, Any]]:
    lines = text.split("\n")
    return _windows(lines, 1, len(lines), size, os.path.basename(path), "config")


CONFIG_EXT = {
    ".yaml",
    ".yml",
    ".json",
    ".toml",
    ".ini",
    ".cfg",
    ".conf",
    ".properties",
    ".xml",
    ".tf",
    ".hcl",
}
CONFIG_NAMES = {"Dockerfile", "Makefile", "docker-compose.yml", "compose.yaml", "Caddyfile", "Procfile"}

# ------------------------------------------------------------------ dependencies

_PEP508 = re.compile(r"^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*(.*)$")


def _pep508(spec: str, manifest: str, dev: bool) -> dict[str, Any] | None:
    m = _PEP508.match(spec.split(";")[0])
    if not m:
        return None
    return {
        "ecosystem": "pypi",
        "name": m.group(1).lower(),
        "version": m.group(3).strip(),
        "dev": dev,
        "manifest": manifest,
    }


def dependencies(path: str, text: str) -> list[dict[str, Any]]:
    """Declared dependencies of a manifest (package.json, go.mod, pyproject, requirements, Cargo.toml)."""
    base = os.path.basename(path)
    out: list[dict[str, Any]] = []
    try:
        if base == "package.json":
            data = json.loads(text)
            for key, dev in (("dependencies", False), ("devDependencies", True), ("peerDependencies", False)):
                for name, ver in (data.get(key) or {}).items():
                    out.append(
                        {"ecosystem": "npm", "name": name, "version": str(ver), "dev": dev, "manifest": path}
                    )
        elif base == "go.mod":
            block = False
            for line in text.splitlines():
                line = line.strip()
                if line.startswith("require ("):
                    block = True
                    continue
                if block and line == ")":
                    block = False
                    continue
                if line.startswith("require ") and not line.endswith("("):
                    line, block_line = line[len("require ") :], True
                else:
                    block_line = block
                if block_line and line and not line.startswith("//") and "// indirect" not in line:
                    parts = line.split()
                    if len(parts) >= 2:
                        out.append(
                            {
                                "ecosystem": "go",
                                "name": parts[0],
                                "version": parts[1],
                                "dev": False,
                                "manifest": path,
                            }
                        )
        elif base == "pyproject.toml":
            data = tomllib.loads(text)
            project = data.get("project", {})
            for spec in project.get("dependencies", []):
                d = _pep508(spec, path, False)
                if d:
                    out.append(d)
            for group in (
                project.get("optional-dependencies", {}) | data.get("dependency-groups", {})
            ).values():
                for spec in group:
                    d = _pep508(spec, path, True) if isinstance(spec, str) else None
                    if d:
                        out.append(d)
            for name, ver in data.get("tool", {}).get("poetry", {}).get("dependencies", {}).items():
                if name != "python":
                    out.append(
                        {
                            "ecosystem": "pypi",
                            "name": name.lower(),
                            "version": str(ver) if isinstance(ver, str) else "",
                            "dev": False,
                            "manifest": path,
                        }
                    )
        elif re.fullmatch(r"requirements[\w.-]*\.txt", base):
            dev = "dev" in base or "test" in base
            for line in text.splitlines():
                line = line.split("#")[0].strip()
                if line and not line.startswith("-"):
                    d = _pep508(line, path, dev)
                    if d:
                        out.append(d)
        elif base == "Cargo.toml":
            data = tomllib.loads(text)
            for key, dev in (
                ("dependencies", False),
                ("dev-dependencies", True),
                ("build-dependencies", True),
            ):
                for name, ver in (data.get(key) or {}).items():
                    version = (
                        ver
                        if isinstance(ver, str)
                        else (ver.get("version", "") if isinstance(ver, dict) else "")
                    )
                    out.append(
                        {"ecosystem": "cargo", "name": name, "version": version, "dev": dev, "manifest": path}
                    )
    except (ValueError, tomllib.TOMLDecodeError, AttributeError, TypeError):
        return []
    return out


MANIFESTS = {"package.json", "go.mod", "pyproject.toml", "Cargo.toml"}


def is_manifest(path: str) -> bool:
    base = os.path.basename(path)
    return base in MANIFESTS or bool(re.fullmatch(r"requirements[\w.-]*\.txt", base))


# ------------------------------------------------------------------ git


def git_head(root: str) -> str:
    """The commit HEAD points at, read from .git (worktrees and packed refs included)."""
    gitdir = os.path.join(root, ".git")
    try:
        if os.path.isfile(gitdir):  # worktree / submodule: "gitdir: <path>"
            with open(gitdir) as f:
                ref = f.read().strip()
            if ref.startswith("gitdir:"):
                gitdir = os.path.normpath(os.path.join(root, ref[7:].strip()))
        with open(os.path.join(gitdir, "HEAD")) as f:
            head = f.read().strip()
        if not head.startswith("ref:"):
            return head
        ref = head[4:].strip()
        common = gitdir
        commondir = os.path.join(gitdir, "commondir")
        if os.path.isfile(commondir):
            with open(commondir) as f:
                common = os.path.normpath(os.path.join(gitdir, f.read().strip()))
        for base in (gitdir, common):
            p = os.path.join(base, ref)
            if os.path.isfile(p):
                with open(p) as f:
                    return f.read().strip()
        packed = os.path.join(common, "packed-refs")
        if os.path.isfile(packed):
            with open(packed) as f:
                for line in f:
                    parts = line.strip().split(" ")
                    if len(parts) == 2 and parts[1] == ref:
                        return parts[0]
    except OSError:
        return ""
    return ""
