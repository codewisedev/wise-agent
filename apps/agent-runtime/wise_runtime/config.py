from __future__ import annotations

import os
import socket
from dataclasses import dataclass, field


@dataclass(frozen=True)
class Config:
    api_url: str
    runtime_token: str
    runtime_id: str
    poll_interval_s: float = 2.0
    request_timeout_s: float = 30.0
    model_timeout_s: float = 1200.0
    # Repositories outside these roots are never read, even if the control plane lists them.
    repo_roots: tuple[str, ...] = field(default_factory=tuple)

    @classmethod
    def from_env(cls) -> Config:
        token = os.environ.get("WISE_RUNTIME_TOKEN", "")
        if not token:
            raise SystemExit("WISE_RUNTIME_TOKEN is required")
        roots = tuple(
            os.path.realpath(p)
            for p in os.environ.get("WISE_REPO_ROOTS", "").replace(",", ":").split(":")
            if p
        )
        return cls(
            api_url=os.environ.get("WISE_API_URL", "http://127.0.0.1:8080").rstrip("/"),
            runtime_token=token,
            runtime_id=os.environ.get("WISE_RUNTIME_ID", f"{socket.gethostname()}-{os.getpid()}"),
            poll_interval_s=float(os.environ.get("WISE_RUNTIME_POLL_INTERVAL", "2")),
            repo_roots=roots,
        )
