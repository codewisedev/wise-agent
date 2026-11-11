#!/usr/bin/env python3
"""Writes the Docker secrets (.secrets/<name>) from .env (make secrets).

.env stays the one place you edit; this mirrors its secret values into files
that docker-compose.yml mounts under /run/secrets. The directory is private
(0700, git-ignored); the files are readable by the container users. Values
are never printed.
"""

from __future__ import annotations

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAMES = [
    "CLAUDE_CODE_OAUTH_TOKEN", "WISE_BOOTSTRAP_ADMIN_PASSWORD", "WISE_CLI_RUNNER_TOKEN", "WISE_EGRESS_TOKEN",
    "WISE_EXECUTOR_TOKEN", "WISE_GIT_PUSH_TOKEN", "WISE_GIT_WORKER_TOKEN", "WISE_LMSTUDIO_API_KEY",
    "WISE_MODEL_PROXY_TOKEN", "WISE_POSTGRES_PASSWORD", "WISE_RUNTIME_TOKEN", "WISE_SECRETS_KEY",
]
REQUIRED = {"WISE_POSTGRES_PASSWORD", "WISE_RUNTIME_TOKEN", "WISE_CLI_RUNNER_TOKEN", "WISE_EXECUTOR_TOKEN",
            "WISE_EGRESS_TOKEN", "WISE_MODEL_PROXY_TOKEN", "WISE_GIT_PUSH_TOKEN", "WISE_GIT_WORKER_TOKEN"}


def read_env(path: str) -> dict[str, str]:
    env: dict[str, str] = {}
    for line in open(path, encoding="utf-8"):
        m = re.match(r"^\s*([A-Z0-9_]+)=(.*)$", line.rstrip("\n"))
        if m:
            v = m.group(2).strip()
            if len(v) >= 2 and v[0] == v[-1] and v[0] in "\"'":
                v = v[1:-1]
            env[m.group(1)] = v
    return env


def main() -> int:
    env = read_env(os.path.join(ROOT, ".env"))
    missing = sorted(n for n in REQUIRED if not env.get(n))
    if missing:
        print("set these in .env first:", ", ".join(missing))
        return 1
    out = os.path.join(ROOT, ".secrets")
    os.makedirs(out, mode=0o700, exist_ok=True)
    os.chmod(out, 0o700)
    for name in NAMES:
        path = os.path.join(out, name.lower())
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(env.get(name, ""))
        os.chmod(tmp, 0o444)  # the directory keeps them private on the host
        os.replace(tmp, path)
    print(f"wrote {len(NAMES)} secrets to .secrets/")
    return 0


if __name__ == "__main__":
    sys.exit(main())
