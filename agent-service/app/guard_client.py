"""Client for Sentinel Guard's action API.

The agent proposes the remediation it wants to run; a human approves it (through the gateway,
which records the decision in the guard on the person's behalf); the guard then hands the agent
a single-use capability for exactly that action, which the remediation adapter redeems once.
The agent never mints or holds standing credentials, and approval lives outside this process.

    SENTINEL_GUARD_URL         e.g. http://sentinel-guard:8080
    SENTINEL_GUARD_TOKEN       a static agent token (single-process guard), or
    SENTINEL_GUARD_TOKEN_FILE  a projected ServiceAccount token (Kubernetes; re-read on every call)
"""
from __future__ import annotations

import os

import httpx

TIMEOUT = httpx.Timeout(5.0)


class GuardError(RuntimeError):
    pass


class GuardClient:
    def __init__(self, url: str, token: str | None = None, token_file: str | None = None):
        self.url = url.rstrip("/")
        self._token, self._token_file = token, token_file

    @classmethod
    def from_env(cls) -> "GuardClient | None":
        url = os.getenv("SENTINEL_GUARD_URL")
        if not url:
            return None
        return cls(url, os.getenv("SENTINEL_GUARD_TOKEN"), os.getenv("SENTINEL_GUARD_TOKEN_FILE"))

    def _headers(self) -> dict:
        token = self._token
        if self._token_file:
            with open(self._token_file) as f:  # kubelet rotates it: read it every time
                token = f.read().strip()
        return {"Authorization": f"Bearer {token}"} if token else {}

    async def _call(self, method: str, path: str, **kwargs) -> dict:
        async with httpx.AsyncClient(timeout=TIMEOUT) as client:
            response = await client.request(method, f"{self.url}{path}", headers=self._headers(), **kwargs)
        body = response.json() if response.headers.get("content-type", "").startswith("application/json") else {}
        # 200 allow, 202 pending, 403 block are all answers; anything else is the guard failing.
        if response.status_code not in (200, 202, 403) or "verdict" not in body:
            raise GuardError(f"Sentinel Guard {method} {path}: HTTP {response.status_code} {body.get('error', '')}".strip())
        return body

    async def propose(self, action: str, tenant: str, service: str, change: dict, intent: str) -> dict:
        """Registers the action; returns the guard's outcome (verdict, request, message)."""
        return await self._call("POST", "/v1/actions", json={
            "action": action, "target": {"namespace": tenant, "name": service},
            "change": {k: str(v) for k, v in change.items()}, "intent": intent[:500]})

    async def capability(self, request_id: str) -> dict:
        """After approval: the single-use capability (verdict allow), issued once."""
        return await self._call("GET", f"/v1/actions/{request_id}")
