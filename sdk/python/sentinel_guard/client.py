"""Clients for Sentinel Guard's action API.

    guard = Guard("http://localhost:8080", token="...")          # or SENTINEL_GUARD_URL / _TOKEN / _TOKEN_FILE
    cap = guard.require("refund", target="order-1042", change={"amount": "42.00 EUR"}, wait=300)
    guard.spend(cap.token, executor="payments")                    # right before acting: once, ever
    issue_refund(...)

`require` asks the guard. Policy may allow the action at once; otherwise a human must approve it
(waiting up to `wait` seconds, or raising ApprovalPending so the agent can tell its user).
`spend` redeems the single-use capability, atomically, at the guard: call it immediately before
the side effect, ideally in the component that performs it.
"""
from __future__ import annotations

import asyncio
import functools
import inspect
import json
import os
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Mapping

import httpx


class GuardError(RuntimeError):
    """Base class: anything that means "do not perform the action"."""


class GuardUnavailable(GuardError):
    """The guard could not be reached or answered unexpectedly. Fail closed: don't act."""


class ApprovalPending(GuardError):
    """A human must approve first. Show `message` (it names the request) and try again later."""

    def __init__(self, request_id: str, message: str):
        super().__init__(message)
        self.request_id, self.message = request_id, message


class ActionBlocked(GuardError):
    """Policy forbids the action, or a human denied it. It will not be approved."""

    def __init__(self, request_id: str | None, message: str, reasons: list[str] | None = None):
        super().__init__(message)
        self.request_id, self.message, self.reasons = request_id, message, reasons or []


class CapabilityRejected(GuardError):
    """The guard refused to redeem a capability (already used, expired, forged)."""

    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


@dataclass
class Decision:
    """The guard's answer to a proposal: verdict allow | pending | block."""

    verdict: str
    message: str
    request: dict = field(default_factory=dict)
    reasons: list[str] = field(default_factory=list)
    capability: str | None = None

    @property
    def request_id(self) -> str | None:
        return self.request.get("id")


@dataclass
class Capability:
    """A single-use authorization for exactly one approved action. Redeem it with `spend`."""

    token: str
    request_id: str
    approved_by: str | None = None

    @property
    def claims(self) -> dict:
        """The token's claims, unverified (the guard verifies when you spend it)."""
        import base64
        payload = self.token.split(".")[1]
        return json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))


def canonical_change(change: Mapping[str, Any] | None) -> dict[str, str]:
    """The approved parameters as the guard binds them: strings as they are, anything else as compact
    JSON with sorted keys. The TypeScript SDK encodes identically, so an approval matches either SDK."""
    return {str(k): v if isinstance(v, str) else json.dumps(v, sort_keys=True, separators=(",", ":"))
            for k, v in (change or {}).items()}


_stringify = canonical_change


def _proposal(action: str, target: str, change: Mapping[str, Any] | None, intent: str, namespace: str | None,
              dry_run: bool) -> dict:
    t = {"name": target}
    if namespace:
        t["namespace"] = namespace
    body = {"action": action, "target": t, "change": _stringify(change), "intent": intent[:500]}
    if dry_run:
        body["dryRun"] = True
    return body


def _decision(response: httpx.Response) -> Decision:
    try:
        body = response.json()
    except ValueError:
        body = {}
    # 200 allow, 202 pending, 403 block are answers; anything else is the guard failing.
    if response.status_code not in (200, 202, 403) or "verdict" not in body:
        raise GuardUnavailable(f"Sentinel Guard: HTTP {response.status_code} {body.get('error', response.text[:200])}".strip())
    return Decision(verdict=body["verdict"], message=body.get("message", ""), request=body.get("request") or {},
                    reasons=body.get("reasons") or [], capability=body.get("capability"))


def _capability(d: Decision) -> Capability:
    if d.verdict == "allow" and d.capability:
        return Capability(d.capability, d.request_id or "", (d.request or {}).get("decidedBy"))
    if d.verdict == "pending":
        raise ApprovalPending(d.request_id or "", d.message)
    raise ActionBlocked(d.request_id, d.message, d.reasons)


def _spent(response: httpx.Response) -> dict:
    if response.status_code == 200:
        return response.json().get("claims", {})
    try:
        message = response.json().get("error", "")
    except ValueError:
        message = response.text[:200]
    if response.status_code in (400, 403, 409):
        raise CapabilityRejected(response.status_code, message or "capability rejected")
    raise GuardUnavailable(f"Sentinel Guard: HTTP {response.status_code} {message}".strip())


class _Base:
    def __init__(self, url: str | None = None, token: str | None = None, token_file: str | None = None,
                 timeout: float = 10.0, poll_interval: float = 2.0):
        self.url = (url or os.getenv("SENTINEL_GUARD_URL") or "http://localhost:8080").rstrip("/")
        self._token = token if token is not None else os.getenv("SENTINEL_GUARD_TOKEN")
        self._token_file = token_file or os.getenv("SENTINEL_GUARD_TOKEN_FILE")
        self.timeout, self.poll_interval = timeout, poll_interval

    def _headers(self) -> dict:
        token = self._token
        if self._token_file:  # a projected ServiceAccount token: rotated by the kubelet, so read it every time
            with open(self._token_file) as f:
                token = f.read().strip()
        return {"Authorization": f"Bearer {token}"} if token else {}


def _bind(fn: Callable, args: tuple, kwargs: dict) -> dict:
    bound = inspect.signature(fn).bind(*args, **kwargs)
    bound.apply_defaults()
    return dict(bound.arguments)


def _resolve(spec: Any, arguments: dict, default: Any = None) -> Any:
    if spec is None:
        return default
    if callable(spec):
        return spec(arguments)
    if isinstance(spec, str) and "{" in spec:
        return spec.format(**arguments)
    return spec


class Guard(_Base):
    """Synchronous client."""

    def propose(self, action: str, target: str, change: Mapping[str, Any] | None = None, intent: str = "",
                namespace: str | None = None, dry_run: bool = False) -> Decision:
        try:
            r = httpx.post(f"{self.url}/v1/actions", headers=self._headers(), timeout=self.timeout,
                           json=_proposal(action, target, change, intent, namespace, dry_run))
        except httpx.HTTPError as e:
            raise GuardUnavailable(f"Sentinel Guard unreachable: {e}") from e
        return _decision(r)

    def status(self, request_id: str) -> Decision:
        """The request's state; once approved, the capability (issued once, to the requesting agent)."""
        try:
            r = httpx.get(f"{self.url}/v1/actions/{request_id}", headers=self._headers(), timeout=self.timeout)
        except httpx.HTTPError as e:
            raise GuardUnavailable(f"Sentinel Guard unreachable: {e}") from e
        return _decision(r)

    def require(self, action: str, target: str, change: Mapping[str, Any] | None = None, intent: str = "",
                namespace: str | None = None, wait: float = 0) -> Capability:
        """Asks the guard and returns a capability, waiting up to `wait` seconds for a human."""
        d = self.propose(action, target, change, intent, namespace)
        deadline = time.monotonic() + wait
        while d.verdict == "pending" and time.monotonic() < deadline:
            time.sleep(min(self.poll_interval, max(0.0, deadline - time.monotonic())))
            d = self.status(d.request_id)
        return _capability(d)

    def spend(self, capability: str, executor: str = "") -> dict:
        """Redeems the capability (atomically, once) and returns its verified claims."""
        try:
            r = httpx.post(f"{self.url}/v1/capabilities/spend", timeout=self.timeout,
                           json={"capability": capability, "executor": executor})
        except httpx.HTTPError as e:
            raise GuardUnavailable(f"Sentinel Guard unreachable, refusing to act: {e}") from e
        return _spent(r)

    def guarded(self, action: str, target: Any, change: Any = None, intent: Any = "", namespace: Any = None,
                wait: float = 0, executor: str | None = None):
        """Decorator: the function runs only once the guard allows exactly this call.

        `target`, `change`, `intent` and `namespace` are values, `"{arg}"` templates, or callables
        taking the call's arguments as a dict. By default the approval is bound to all arguments.
        """
        def decorator(fn):
            @functools.wraps(fn)
            def wrapper(*args, **kwargs):
                a = _bind(fn, args, kwargs)
                cap = self.require(action, str(_resolve(target, a)), _resolve(change, a, a), _resolve(intent, a, ""),
                                   _resolve(namespace, a), wait=wait)
                self.spend(cap.token, executor or fn.__name__)
                return fn(*args, **kwargs)
            return wrapper
        return decorator


class AsyncGuard(_Base):
    """Asynchronous client (asyncio)."""

    async def _request(self, method: str, path: str, **kwargs) -> httpx.Response:
        try:
            async with httpx.AsyncClient(timeout=self.timeout) as client:
                return await client.request(method, f"{self.url}{path}", **kwargs)
        except httpx.HTTPError as e:
            raise GuardUnavailable(f"Sentinel Guard unreachable: {e}") from e

    async def propose(self, action: str, target: str, change: Mapping[str, Any] | None = None, intent: str = "",
                      namespace: str | None = None, dry_run: bool = False) -> Decision:
        return _decision(await self._request("POST", "/v1/actions", headers=self._headers(),
                                             json=_proposal(action, target, change, intent, namespace, dry_run)))

    async def status(self, request_id: str) -> Decision:
        return _decision(await self._request("GET", f"/v1/actions/{request_id}", headers=self._headers()))

    async def require(self, action: str, target: str, change: Mapping[str, Any] | None = None, intent: str = "",
                      namespace: str | None = None, wait: float = 0) -> Capability:
        d = await self.propose(action, target, change, intent, namespace)
        deadline = time.monotonic() + wait
        while d.verdict == "pending" and time.monotonic() < deadline:
            await asyncio.sleep(min(self.poll_interval, max(0.0, deadline - time.monotonic())))
            d = await self.status(d.request_id)
        return _capability(d)

    async def spend(self, capability: str, executor: str = "") -> dict:
        return _spent(await self._request("POST", "/v1/capabilities/spend",
                                          json={"capability": capability, "executor": executor}))

    def guarded(self, action: str, target: Any, change: Any = None, intent: Any = "", namespace: Any = None,
                wait: float = 0, executor: str | None = None):
        """Decorator for async functions; see Guard.guarded."""
        def decorator(fn):
            @functools.wraps(fn)
            async def wrapper(*args, **kwargs):
                a = _bind(fn, args, kwargs)
                cap = await self.require(action, str(_resolve(target, a)), _resolve(change, a, a),
                                         _resolve(intent, a, ""), _resolve(namespace, a), wait=wait)
                await self.spend(cap.token, executor or fn.__name__)
                return await fn(*args, **kwargs)
            return wrapper
        return decorator
