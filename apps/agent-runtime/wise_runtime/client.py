"""HTTP client for the control plane's runtime API (/api/v1/runtime/v1)."""

from __future__ import annotations

from typing import Any

import httpx


class ApiError(Exception):
    def __init__(self, status: int, detail: str, body: dict[str, Any] | None = None):
        super().__init__(f"{status}: {detail}")
        self.status = status
        self.detail = detail
        self.body = body or {}


class ControlPlane:
    def __init__(
        self,
        api_url: str,
        runtime_token: str,
        *,
        timeout_s: float = 30.0,
        model_timeout_s: float = 1200.0,
        transport: httpx.BaseTransport | None = None,
    ):
        self._base = f"{api_url}/api/v1/runtime/v1"
        self._runtime_token = runtime_token
        self._timeout = timeout_s
        self._model_timeout = model_timeout_s
        self._http = httpx.Client(timeout=timeout_s, transport=transport)

    def close(self) -> None:
        self._http.close()

    def _call(
        self, method: str, path: str, token: str, body: Any = None, timeout: float | None = None
    ) -> dict[str, Any] | None:
        res = self._http.request(
            method,
            self._base + path,
            json=body,
            headers={"Authorization": f"Bearer {token}"},
            timeout=timeout or self._timeout,
        )
        if res.status_code == 204:
            return None
        try:
            data = res.json()
        except ValueError:
            data = {}
        if res.status_code >= 300:
            detail = data.get("detail") or data.get("title") or res.text[:300]
            if data.get("errors"):
                detail += " — " + "; ".join(f"{e['field']}: {e['message']}" for e in data["errors"])
            raise ApiError(res.status_code, detail, data)
        return data

    def claim(self, runtime_id: str) -> dict[str, Any] | None:
        return self._call("POST", "/runs/claim", self._runtime_token, {"runtimeId": runtime_id})

    def heartbeat(self, run_id: str, token: str) -> dict[str, Any]:
        return self._call("POST", f"/runs/{run_id}/heartbeat", token) or {}

    def append_events(self, run_id: str, token: str, events: list[dict[str, Any]]) -> None:
        for i in range(0, len(events), 100):
            self._call("POST", f"/runs/{run_id}/events", token, {"events": events[i : i + 100]})

    def model_turn(self, run_id: str, token: str, messages: list[dict[str, str]]) -> dict[str, Any]:
        return (
            self._call(
                "POST",
                f"/runs/{run_id}/model-turn",
                token,
                {"messages": messages},
                timeout=self._model_timeout,
            )
            or {}
        )

    def complete(self, run_id: str, token: str, output: dict[str, Any]) -> None:
        self._call("POST", f"/runs/{run_id}/complete", token, {"output": output})

    def fail(
        self, run_id: str, token: str, error: str, *, retryable: bool = False, cancelled: bool = False
    ) -> None:
        self._call(
            "POST",
            f"/runs/{run_id}/fail",
            token,
            {"error": error[:10000], "retryable": retryable, "cancelled": cancelled},
        )

    def context(self, run_id: str, token: str, kind: str, params: dict[str, Any]) -> dict[str, Any]:
        """Index and memory queries for a run (search, symbols, dependencies, memory)."""
        path = "memory/search" if kind == "memory" else f"context/{kind}"
        res = self._http.get(
            f"{self._base}/runs/{run_id}/{path}",
            params=params,
            headers={"Authorization": f"Bearer {token}"},
            timeout=self._timeout,
        )
        try:
            data = res.json()
        except ValueError:
            data = {}
        if res.status_code >= 300:
            detail = data.get("detail") or data.get("title") or res.text[:300]
            raise ApiError(res.status_code, detail, data)
        return data
