"""Claims runs from the control plane and executes them one at a time."""

from __future__ import annotations

import logging
import signal
import threading
import time
import traceback
from typing import Any

from .client import ApiError, ControlPlane
from .config import Config
from .events import EventEmitter
from .framework import AgentFramework, RunCancelled, RunContext, RunFailed
from .framework.wise_loop import WiseLoop
from .tools import ToolRegistry
from .tools.workspace import Workspace

log = logging.getLogger(__name__)


class Heartbeat(threading.Thread):
    """Extends the run lease; sets `cancel` when cancellation is requested or the lease is lost."""

    def __init__(
        self, client: ControlPlane, run_id: str, token: str, interval_s: float, cancel: threading.Event
    ):
        super().__init__(daemon=True, name=f"heartbeat-{run_id[:8]}")
        self._client, self._run_id, self._token = client, run_id, token
        self._interval = interval_s
        self._cancel = cancel
        self._stop_event = threading.Event()
        self.lost = False

    def run(self) -> None:
        while not self._stop_event.wait(self._interval):
            try:
                if self._client.heartbeat(self._run_id, self._token).get("cancelRequested"):
                    log.info("cancellation requested", extra={"run_id": self._run_id})
                    self._cancel.set()
            except ApiError as e:
                if e.status in (401, 403):
                    log.warning("lease lost", extra={"run_id": self._run_id})
                    self.lost = True
                    self._cancel.set()
                    return
            except Exception:
                log.warning("heartbeat failed", exc_info=True)

    def stop(self) -> None:
        self._stop_event.set()


class Worker:
    def __init__(
        self, config: Config, client: ControlPlane | None = None, framework: AgentFramework | None = None
    ):
        self.config = config
        self.client = client or ControlPlane(
            config.api_url,
            config.runtime_token,
            timeout_s=config.request_timeout_s,
            model_timeout_s=config.model_timeout_s,
        )
        self.framework = framework or WiseLoop()
        self._stopping = threading.Event()
        self._current_cancel: threading.Event | None = None

    def stop(self, *_: Any) -> None:
        log.info("shutting down")
        self._stopping.set()
        if self._current_cancel:
            self._current_cancel.set()

    def run_forever(self) -> None:
        signal.signal(signal.SIGTERM, self.stop)
        signal.signal(signal.SIGINT, self.stop)
        idle = self.config.poll_interval_s
        while not self._stopping.is_set():
            try:
                worked = self.poll_once()
            except Exception:
                log.exception("claim failed")
                worked = False
            idle = self.config.poll_interval_s if worked else min(idle * 1.5, 15.0)
            if not worked:
                self._stopping.wait(idle)

    def poll_once(self) -> bool:
        claim = self.client.claim(self.config.runtime_id)
        if not claim:
            return False
        self.execute(claim)
        return True

    def execute(self, claim: dict[str, Any]) -> None:
        run, token = claim["run"], claim["runToken"]
        run_id = run["id"]
        definition = claim["definition"]
        log.info(
            "run claimed", extra={"run_id": run_id, "agent": run["agentName"], "attempt": run["attempt"]}
        )

        cancel = threading.Event()
        self._current_cancel = cancel
        events = EventEmitter(self.client, run_id, token)
        heartbeat = Heartbeat(self.client, run_id, token, max(claim.get("leaseSeconds", 60) / 3, 1), cancel)
        heartbeat.start()
        try:
            workspace = Workspace(claim.get("project"), self.config.repo_roots)
            ctx = RunContext(
                run=run,
                definition=definition,
                prompt=claim.get("prompt", ""),
                tools=self._registry(claim.get("tools", []), workspace),
                events=events,
                model_turn=lambda messages: self.client.model_turn(run_id, token, messages),
                project=claim.get("project"),
                cancel=cancel,
                deadline=time.monotonic()
                + float(definition.get("execution_limits", {}).get("timeout_s", 600)),
            )
            output = self.framework.run(ctx)
            events.flush()
            self.client.complete(run_id, token, output)
            log.info("run succeeded", extra={"run_id": run_id})
        except RunCancelled:
            events.flush()
            if heartbeat.lost:
                log.warning("run abandoned after losing its lease", extra={"run_id": run_id})
            elif self._stopping.is_set():
                self._safe_fail(run_id, token, "runtime shutting down", retryable=True)
            else:
                self._safe_fail(run_id, token, "cancelled by user", cancelled=True)
        except RunFailed as e:
            events.flush()
            log.warning("run failed: %s", e, extra={"run_id": run_id})
            self._safe_fail(run_id, token, str(e), retryable=e.retryable)
        except ApiError as e:
            events.flush()
            # 429 = budget exhausted, 422 = output rejected: retrying will not help; 5xx may.
            retryable = e.status >= 500
            log.warning("control plane rejected the run: %s", e, extra={"run_id": run_id})
            self._safe_fail(run_id, token, f"control plane error {e.status}: {e.detail}", retryable=retryable)
        except Exception as e:
            events.flush()
            log.exception("run crashed", extra={"run_id": run_id})
            tb = "".join(traceback.format_exception_only(e)).strip()
            self._safe_fail(run_id, token, f"runtime error: {tb}", retryable=True)
        finally:
            heartbeat.stop()
            self._current_cancel = None

    @staticmethod
    def _registry(tools: list[dict[str, Any]], workspace: Workspace) -> ToolRegistry:
        # A definition this runtime cannot serve fails the same way on every attempt.
        try:
            return ToolRegistry(tools, workspace)
        except ValueError as e:
            raise RunFailed(f"runtime cannot run this agent: {e}") from e

    def _safe_fail(
        self, run_id: str, token: str, error: str, *, retryable: bool = False, cancelled: bool = False
    ) -> None:
        try:
            self.client.fail(run_id, token, error, retryable=retryable, cancelled=cancelled)
        except ApiError as e:
            log.warning("could not report failure: %s", e, extra={"run_id": run_id})
