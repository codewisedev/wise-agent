"""Index one repository: walk, hash, parse changed files, upload in batches.

The worker reads the repository read-only and never touches the database: it
sends files, symbols, chunks and dependencies to the control plane, which
stores them against the commit the indexer saw.
"""

from __future__ import annotations

import hashlib
import logging
import os
import re
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any

import httpx
import pathspec

from ..client import ApiError
from ..tools.workspace import _within
from .content import (
    CONFIG_EXT,
    CONFIG_NAMES,
    code_chunks,
    config_chunks,
    dependencies,
    doc_chunks,
    git_head,
    is_manifest,
    looks_secret,
)
from .symbols import language_for, parse

log = logging.getLogger(__name__)

BATCH_FILES = 100
BATCH_CHUNKS = 3000  # the server accepts up to 5000 per batch
MAX_FILE_CHUNKS = 2000
MAX_FILE_SYMBOLS = 4000
INDEXER_VERSION = 1  # bump when extraction changes, so every file is re-indexed
TEXT_EXT = {
    ".md",
    ".mdx",
    ".rst",
    ".adoc",
    ".txt",
    ".sql",
    ".graphql",
    ".gql",
    ".proto",
    ".sh",
    ".bash",
    ".zsh",
    ".css",
    ".scss",
    ".less",
    ".html",
    ".svelte",
    ".astro",
    ".rb",
    ".php",
    ".kt",
    ".kts",
    ".swift",
    ".c",
    ".h",
    ".cc",
    ".cpp",
    ".hpp",
    ".cs",
    ".scala",
    ".lua",
    ".dart",
    ".ex",
    ".exs",
    ".erl",
    ".clj",
    ".r",
} | CONFIG_EXT


@dataclass
class Settings:
    exclude: list[str] = field(default_factory=list)
    include: list[str] = field(default_factory=list)
    docs: list[str] = field(default_factory=list)
    max_file_kb: int = 512
    max_files: int = 20000
    chunk_lines: int = 60
    secret_patterns: list[str] = field(default_factory=list)

    @classmethod
    def from_json(cls, d: dict[str, Any]) -> Settings:
        return cls(
            exclude=list(d.get("exclude") or []),
            include=list(d.get("include") or []),
            docs=list(d.get("docs") or []),
            max_file_kb=int(d.get("max_file_kb") or 512),
            max_files=int(d.get("max_files") or 20000),
            chunk_lines=int(d.get("chunk_lines") or 60),
            secret_patterns=list(d.get("secret_patterns") or []),
        )


def _spec(lines: list[str]) -> pathspec.PathSpec:
    return pathspec.GitIgnoreSpec.from_lines(lines)


class Ignores:
    """The repository's .gitignore files (root and nested) plus the configured excludes."""

    def __init__(self, root: str, exclude: list[str]):
        self.root = root
        self.exclude = _spec([".git/", *exclude])
        self.by_dir: dict[str, pathspec.PathSpec] = {}

    def load(self, rel_dir: str) -> None:
        p = os.path.join(self.root, rel_dir, ".gitignore")
        if os.path.isfile(p):
            try:
                with open(p, encoding="utf-8", errors="replace") as f:
                    self.by_dir[rel_dir] = _spec(f.read().splitlines())
            except OSError:
                pass

    def ignored(self, rel: str, is_dir: bool) -> bool:
        probe = rel + "/" if is_dir else rel
        if self.exclude.match_file(probe):
            return True
        for d, spec in self.by_dir.items():
            if d == "" or rel.startswith(d + "/"):
                sub = rel[len(d) + 1 :] if d else rel
                if spec.match_file(sub + "/" if is_dir else sub):
                    return True
        return False


def walk(root: str, settings: Settings) -> tuple[list[str], bool]:
    """Repository-relative file paths to consider, and whether max_files cut the walk short."""
    ign = Ignores(root, settings.exclude)
    include = _spec(settings.include) if settings.include else None
    files: list[str] = []
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        rel_dir = os.path.relpath(dirpath, root)
        rel_dir = "" if rel_dir == "." else rel_dir.replace(os.sep, "/")
        ign.load(rel_dir)
        keep = []
        for d in sorted(dirnames):
            rel = f"{rel_dir}/{d}" if rel_dir else d
            if not os.path.islink(os.path.join(dirpath, d)) and not ign.ignored(rel, True):
                keep.append(d)
        dirnames[:] = keep
        for name in sorted(filenames):
            rel = f"{rel_dir}/{name}" if rel_dir else name
            full = os.path.join(dirpath, name)
            if os.path.islink(full) or ign.ignored(rel, False):
                continue
            if include is not None and not include.match_file(rel) and not is_manifest(rel):
                continue
            files.append(rel)
            if len(files) >= settings.max_files:
                return files, True
    return files, False


def is_text_candidate(rel: str) -> bool:
    base = os.path.basename(rel)
    ext = os.path.splitext(base)[1].lower()
    return bool(language_for(rel)) or ext in TEXT_EXT or base in CONFIG_NAMES or is_manifest(rel)


def fingerprint(settings: Settings) -> bytes:
    """What changes a file's index besides its content: a settings change re-processes every file
    (e.g. a new secret pattern purges files indexed before it existed)."""
    keys = (
        settings.exclude,
        settings.include,
        settings.docs,
        settings.max_file_kb,
        settings.chunk_lines,
        settings.secret_patterns,
        INDEXER_VERSION,
    )
    return hashlib.sha256(repr(keys).encode()).digest()


def file_hash(fp: bytes, data: bytes) -> str:
    return hashlib.sha256(fp + data).hexdigest()


def index_file(
    rel: str,
    data: bytes,
    settings: Settings,
    docs: pathspec.PathSpec,
    secret_rx: list[re.Pattern[str]],
    fp: bytes = b"",
) -> dict[str, Any]:
    """The upload record of one file: metadata plus symbols and chunks (or why it was skipped)."""
    rec: dict[str, Any] = {
        "path": rel,
        "contentHash": file_hash(fp, data),
        "size": len(data),
        "language": language_for(rel),
        "lines": data.count(b"\n") + 1 if data else 0,
        "imports": [],
        "symbols": [],
        "chunks": [],
        "skippedReason": "",
    }
    if len(data) > settings.max_file_kb * 1024:
        rec["skippedReason"] = "too_large"
        return rec
    if b"\x00" in data[:8000]:
        rec["skippedReason"] = "binary"
        return rec
    text = data.decode("utf-8", "replace")
    if looks_secret(rel, text, secret_rx):
        rec["skippedReason"] = "secret"
        return rec
    ext = os.path.splitext(rel)[1].lower()
    if docs.match_file(rel):
        rec["language"] = rec["language"] or "markdown"
        rec["chunks"] = doc_chunks(text, settings.chunk_lines)
    elif rec["language"]:
        parsed = parse(rel, data)
        rec["symbols"] = [s.as_json() for s in parsed.symbols]
        rec["imports"] = parsed.imports
        rec["chunks"] = code_chunks(text, parsed.symbols, settings.chunk_lines)
    elif ext in CONFIG_EXT or os.path.basename(rel) in CONFIG_NAMES or is_manifest(rel):
        rec["chunks"] = config_chunks(text, rel, settings.chunk_lines)
    else:
        rec["chunks"] = code_chunks(text, [], settings.chunk_lines)
    # Generated or minified files can be huge: keep the upload within the server's limits.
    rec["symbols"] = rec["symbols"][:MAX_FILE_SYMBOLS]
    rec["chunks"] = rec["chunks"][:MAX_FILE_CHUNKS]
    return rec


@dataclass
class Result:
    seen: int = 0
    uploaded: int = 0
    unchanged: int = 0
    deleted: int = 0
    skipped: int = 0
    truncated: bool = False
    dependencies: list[dict[str, Any]] = field(default_factory=list)
    commit: str = ""


def index_repository(
    root: str,
    settings: Settings,
    manifest: dict[str, str],
    upload: Callable[[list[dict[str, Any]], list[str]], None],
    cancelled: Callable[[], bool] = lambda: False,
) -> Result:
    """Walk the repository, upload changed files in batches, then the deleted paths."""
    res = Result(commit=git_head(root))
    paths, res.truncated = walk(root, settings)
    docs = _spec(settings.docs)
    fp = fingerprint(settings)
    secret_rx = []
    for p in settings.secret_patterns:
        try:
            secret_rx.append(re.compile(p))
        except re.error:
            log.warning("ignoring invalid secret pattern %r", p)
    batch: list[dict[str, Any]] = []
    chunks = 0
    for rel in paths:
        if cancelled():
            raise InterruptedError("cancelled")
        if not is_text_candidate(rel):
            continue
        full = os.path.join(root, rel)
        try:
            with open(full, "rb") as f:
                data = f.read(settings.max_file_kb * 1024 + 1)
        except OSError:
            continue
        res.seen += 1
        digest = file_hash(fp, data)
        if is_manifest(rel) and len(data) <= settings.max_file_kb * 1024:
            res.dependencies += dependencies(rel, data.decode("utf-8", "replace"))
        if manifest.get(rel) == digest:
            res.unchanged += 1
            continue
        rec = index_file(rel, data, settings, docs, secret_rx, fp)
        if rec["skippedReason"]:
            res.skipped += 1
        if batch and chunks + len(rec["chunks"]) > BATCH_CHUNKS:
            upload(batch, [])
            res.uploaded += len(batch)
            batch, chunks = [], 0
        batch.append(rec)
        chunks += len(rec["chunks"])
        if len(batch) >= BATCH_FILES or chunks >= BATCH_CHUNKS:
            upload(batch, [])
            res.uploaded += len(batch)
            batch, chunks = [], 0
    seen = set(paths)
    deleted = [p for p in manifest if p not in seen]
    # A truncated walk did not see every file: keep the rest rather than delete it.
    if res.truncated:
        deleted = []
    for i in range(0, max(len(deleted), 1), 5000):
        part = deleted[i : i + 5000]
        if batch or part:
            upload(batch, part)
            res.uploaded += len(batch)
            batch = []
    res.deleted = len(deleted)
    return res


# ------------------------------------------------------------------ worker


class IndexAPI:
    def __init__(self, api_url: str, runtime_token: str, transport: httpx.BaseTransport | None = None):
        self._base = f"{api_url}/api/v1/runtime/v1/index"
        self._token = runtime_token
        self._http = httpx.Client(timeout=120.0, transport=transport)

    def _call(self, path: str, token: str, body: Any = None) -> dict[str, Any] | None:
        res = self._http.post(self._base + path, json=body, headers={"Authorization": f"Bearer {token}"})
        if res.status_code == 204:
            return None
        try:
            data = res.json() if res.content else {}
        except ValueError:
            data = {}
        if res.status_code >= 300:
            detail = data.get("detail") or data.get("title") or res.text[:300]
            if data.get("errors"):
                detail += " — " + "; ".join(f"{e['field']}: {e['message']}" for e in data["errors"])
            raise ApiError(res.status_code, detail, data)
        return data

    def claim(self, runtime_id: str) -> dict[str, Any] | None:
        return self._call("/claim", self._token, {"runtimeId": runtime_id})

    def heartbeat(self, job: str, token: str) -> None:
        self._call(f"/{job}/heartbeat", token)

    def upload(self, job: str, token: str, files: list[dict[str, Any]], deleted: list[str]) -> None:
        self._call(f"/{job}/files", token, {"files": files, "deleted": deleted})

    def complete(
        self, job: str, token: str, commit: str, deps: list[dict[str, Any]], stats: dict[str, Any]
    ) -> None:
        self._call(f"/{job}/complete", token, {"commitSha": commit, "dependencies": deps, "stats": stats})

    def fail(self, job: str, token: str, error: str) -> None:
        self._call(f"/{job}/fail", token, {"error": error[:4000]})


class IndexWorker:
    def __init__(
        self, api: IndexAPI, runtime_id: str, repo_roots: tuple[str, ...], poll_interval_s: float = 5.0
    ):
        self.api = api
        self.runtime_id = runtime_id
        self.repo_roots = repo_roots
        self.poll = poll_interval_s
        self._stop = threading.Event()

    def stop(self, *_: Any) -> None:
        self._stop.set()

    def run_forever(self) -> None:
        while not self._stop.is_set():
            try:
                worked = self.poll_once()
            except Exception:
                log.exception("index poll failed")
                worked = False
            if not worked:
                self._stop.wait(self.poll)

    def poll_once(self) -> bool:
        claim = self.api.claim(self.runtime_id)
        if not claim:
            return False
        self.execute(claim)
        return True

    def execute(self, claim: dict[str, Any]) -> None:
        job, token = claim["jobId"], claim["token"]
        repo = claim["repository"]
        started = time.monotonic()
        done = threading.Event()
        lost = threading.Event()

        def beat() -> None:
            interval = max(claim.get("leaseSeconds", 120) / 3, 5)
            while not done.wait(interval):
                try:
                    self.api.heartbeat(job, token)
                except ApiError as e:
                    if e.status in (401, 403):
                        lost.set()
                        return
                    log.warning("index heartbeat rejected: %s", e, extra={"job": job})
                except Exception:  # keep beating: a lost lease hands the job to another worker
                    log.exception("index heartbeat failed", extra={"job": job})

        hb = threading.Thread(target=beat, daemon=True)
        hb.start()
        try:
            root = os.path.realpath(repo["localPath"])
            if self.repo_roots and not any(_within(root, r) for r in self.repo_roots):
                raise PermissionError("repository is outside the indexer's allowed roots (WISE_REPO_ROOTS)")
            if not os.path.isdir(root):
                raise FileNotFoundError("repository path is not mounted in the indexer")
            settings = Settings.from_json(claim.get("settings") or {})
            log.info("indexing %s", repo["name"], extra={"job": job})
            res = index_repository(
                root,
                settings,
                claim.get("manifest") or {},
                lambda files, deleted: self.api.upload(job, token, files, deleted),
                cancelled=lambda: self._stop.is_set() or lost.is_set(),
            )
            stats = {
                "seen": res.seen,
                "uploaded": res.uploaded,
                "unchanged": res.unchanged,
                "deleted": res.deleted,
                "skippedThisRun": res.skipped,
                "truncated": res.truncated,
                "seconds": round(time.monotonic() - started, 1),
            }
            self.api.complete(job, token, res.commit, res.dependencies, stats)
            log.info("indexed %s: %s", repo["name"], stats, extra={"job": job})
        except InterruptedError:
            log.warning("index job interrupted", extra={"job": job})
        except Exception as e:  # report every failure on the job
            log.exception("index job failed", extra={"job": job})
            try:
                self.api.fail(job, token, f"{type(e).__name__}: {e}")
            except Exception:
                log.exception("could not report index failure")
        finally:
            done.set()
