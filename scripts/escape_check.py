#!/usr/bin/env python3
"""Sandbox escape checks against the running stack (make escape-check REPO=<path>).

Prepares a throwaway workspace of REPO (a git repository under WISE_REPO_ROOT)
through the executor, runs a probe inside a real runner container and asserts
the isolation rules of ADR 0007/0008; then removes the workspace. No model is
called. The executor is only reachable on the internal `wise_exec` network,
so requests go through a short-lived curl container on that network.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import uuid

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ENV = dict(
    line.rstrip("\n").split("=", 1)
    for line in open(os.path.join(ROOT, ".env"))
    if "=" in line and not line.lstrip().startswith("#")
)
TOKEN = ENV["WISE_EXECUTOR_TOKEN"]


def ex(method: str, path: str, body: dict | None = None) -> dict:
    cmd = ["docker", "run", "--rm", "--network", "wise_exec", "-e", "T", "curlimages/curl:latest", "-s", "-X", method,
           "-H", "Authorization: Bearer " + TOKEN, "-H", "Content-Type: application/json"]
    if body is not None:
        cmd += ["--data", json.dumps(body)]
    out = subprocess.run(cmd + ["http://wise-executor:8091" + path], capture_output=True, text=True).stdout
    return json.loads(out) if out.strip().startswith("{") else {}


def wait(job: str) -> tuple[dict, str]:
    out, off, st = "", 0, {}
    for _ in range(60):
        st = ex("GET", f"/v1/jobs/{job}?offset={off}&wait=20")
        out += st.get("output", "")
        off = st.get("nextOffset", off)
        if st.get("status") != "running" and not st.get("more"):
            break
    return st, out


PROBE = r"""
echo "uid=$(id -u)"
echo "git-entries=$(ls -A /workspace/.git 2>/dev/null | wc -l)"
touch /opt/cli/x 2>/dev/null && echo cli-writable=yes || echo cli-writable=no
touch /usr/x 2>/dev/null && echo rootfs-writable=yes || echo rootfs-writable=no
[ -e /var/run/docker.sock ] && echo docker-socket=yes || echo docker-socket=no
[ -e /workspaces ] && echo other-workspaces=yes || echo other-workspaces=no
echo "secret-env=$(env | grep -cE '=(sk-|sk-ant-|ghp_|github_pat_|xox[baprs]-|AIza)' )"
cat > /tmp/c.mjs <<'JS'
import net from 'node:net';
const u = new URL(process.env.HTTPS_PROXY);
const auth = Buffer.from(decodeURIComponent(u.username) + ':' + decodeURIComponent(u.password)).toString('base64');
const [label, host, mode, port] = process.argv.slice(2);
const s = mode === 'direct' ? net.connect(Number(port), host) : net.connect(Number(u.port), u.hostname);
const done = (r) => { console.log(`${label}=${r}`); process.exit(0); };
s.setTimeout(8000, () => done('timeout'));
s.on('error', (e) => done(e.code));
s.on('connect', () => {
  if (mode === 'direct') return done('connected');
  s.write(`CONNECT ${host}:443 HTTP/1.1\r\nHost: ${host}:443\r\n` + (mode === 'auth' ? `Proxy-Authorization: Basic ${auth}\r\n` : '') + '\r\n');
});
s.on('data', (d) => done(d.toString().split(' ')[1]));
JS
node /tmp/c.mjs granted api.anthropic.com auth
node /tmp/c.mjs granted-without-token api.anthropic.com noauth
node /tmp/c.mjs not-granted example.com auth
node /tmp/c.mjs registry registry.npmjs.org noauth
node /tmp/c.mjs private-direct 172.17.0.1 direct 5173
node /tmp/c.mjs public-direct 1.1.1.1 direct 443
"""

EXPECT = {
    "uid": lambda v: v != "0",
    "git-entries": lambda v: v == "0",
    "cli-writable": lambda v: v == "no",
    "rootfs-writable": lambda v: v == "no",
    "docker-socket": lambda v: v == "no",
    "other-workspaces": lambda v: v == "no",
    "secret-env": lambda v: v == "0",
    "granted": lambda v: v == "200",
    "granted-without-token": lambda v: v == "403",
    "not-granted": lambda v: v == "403",
    "registry": lambda v: v == "200",
    "private-direct": lambda v: v in ("ENETUNREACH", "EHOSTUNREACH", "timeout"),
    "public-direct": lambda v: v in ("ENETUNREACH", "EHOSTUNREACH", "timeout"),
}


def main() -> int:
    repo = sys.argv[1] if len(sys.argv) > 1 else ""
    if not repo:
        print("usage: escape_check.py <git repository under WISE_REPO_ROOT>")
        return 2
    ws = str(uuid.uuid4())
    job = ex("POST", "/v1/workspaces", {"id": ws, "repoPath": repo, "branch": "wise/escape-check", "image": "wise-toolchain-node:22",
                                        "limits": {"cpus": 1, "memoryMb": 1024, "pids": 256}, "tools": ["node"]})
    if "jobId" not in job:
        print("could not prepare a workspace:", job)
        return 1
    try:
        st, _ = wait(job["jobId"])
        if st.get("status") != "succeeded":
            print("prepare failed:", st.get("error"))
            return 1
        run = ex("POST", f"/v1/workspaces/{ws}/runners", {"cli": "claude-code", "version": "2.1.319", "command": ["sh", "-c", PROBE],
                                                         "grantHosts": ["api.anthropic.com"], "wallClockS": 180, "idleS": 60})
        st, out = wait(run["jobId"])
        seen = dict(line.split("=", 1) for line in out.splitlines() if "=" in line and not line.startswith("["))
        failed = 0
        for key, ok in EXPECT.items():
            value = seen.get(key, "missing")
            good = ok(value)
            failed += not good
            print(f"{'PASS' if good else 'FAIL'}  {key:22} {value}")
        time.sleep(1)
        left = subprocess.run(["docker", "ps", "-aq", "--filter", "label=wise.role=runner"], capture_output=True, text=True).stdout.split()
        print(f"{'PASS' if not left else 'FAIL'}  {'runner removed':22} {len(left)} left")
        return 1 if failed or left else 0
    finally:
        ex("DELETE", f"/v1/workspaces/{ws}")


if __name__ == "__main__":
    sys.exit(main())
