"""Remediation MCP server: the only component that changes production.

Every tool call must carry a **capability token** that the gateway mints at the moment a human
approves an incident's proposal. The token is bound to one tenant, one incident, one action and one
target service, lives 5 minutes, and is **single-use**. So even a compromised agent can only
execute exactly what a human approved, once.

    Authorization: Bearer <capability token>
    claims: aud=sentinel-remediation, scope=remediation:execute, action, target, incident_id, tenant_id, jti, exp

Replay protection fails CLOSED (no replay store → no execution): the opposite trade-off to rate
limiting, because a duplicated rollback is worse than a delayed one. Dry runs validate the
capability without consuming it or touching the service.

In production the same tool contracts would front the real deploy system (Argo Rollouts, kubectl,
the feature-flag service); here they drive chaos-lab's /ops endpoints.
"""
from __future__ import annotations

import json
import logging
import os
import time
from datetime import datetime, timezone

import httpx
import jwt

from gateway_keys import GatewayKeys
from mcp.server.fastmcp import Context, FastMCP
from starlette.requests import Request
from starlette.responses import JSONResponse

AUDIENCE = "sentinel-remediation"
SCOPE = "remediation:execute"
MAX_POOL = 60
MAX_REPLICAS = 10  # db-connection-pool runbook: never above 60 per pod
TIMEOUT = httpx.Timeout(10.0)
log = logging.getLogger("sentinel.remediation")
logging.basicConfig(level=logging.INFO, format="%(message)s")

mcp = FastMCP(
    "sentinel-remediation",
    instructions="Executes approved remediations. Every call needs a capability token minted at approval.",
    host=os.getenv("HOST", "127.0.0.1"),
    port=int(os.getenv("PORT", "8300")),
    stateless_http=True,
    json_response=True,
)


class CapabilityError(PermissionError):
    pass


# --- replay store --------------------------------------------------------------------------------

class MemoryReplayStore:
    def __init__(self):
        self.used: dict[str, float] = {}

    def claim(self, jti: str, ttl_s: int) -> bool:
        now = time.time()
        self.used = {k: v for k, v in self.used.items() if v > now}
        if jti in self.used:
            return False
        self.used[jti] = now + ttl_s
        return True


class RedisReplayStore:
    def __init__(self, url: str):
        import redis
        self.redis = redis.Redis.from_url(url, socket_timeout=1.0, socket_connect_timeout=1.0)

    def claim(self, jti: str, ttl_s: int) -> bool:
        return bool(self.redis.set(f"capability:{jti}", "1", nx=True, ex=max(ttl_s, 1)))


def replay_store():
    url = os.getenv("SENTINEL_REPLAY_STORE", "redis://localhost:6379/0")
    return MemoryReplayStore() if url == "memory" else RedisReplayStore(url)


STORE = replay_store()
GATEWAY = GatewayKeys()


# --- capability check ----------------------------------------------------------------------------

def authorize(token: str | None, action: str, service: str, dry_run: bool) -> dict:
    """Validates the capability for exactly this action on exactly this service; consumes it unless dry run."""
    if not token:
        raise CapabilityError("missing capability token")
    try:
        # ES256 against the gateway's public keys: this adapter can verify capabilities but never mint one.
        claims = GATEWAY.verify(token, AUDIENCE)
    except jwt.PyJWTError as e:
        raise CapabilityError(f"invalid capability token: {e}") from e
    if "jti" not in claims:
        raise CapabilityError("invalid capability token: no jti")
    if SCOPE not in str(claims.get("scope", "")).split():
        raise CapabilityError("token lacks remediation:execute")
    if claims.get("action") != action or claims.get("target") != service:
        raise CapabilityError(f"capability covers {claims.get('action')} on {claims.get('target')}, "
                              f"not {action} on {service}")
    if not dry_run:
        try:
            first_use = STORE.claim(claims["jti"], int(claims["exp"] - time.time()) + 60)
        except Exception as e:  # replay store down: refuse (fail closed)
            raise CapabilityError(f"replay protection unavailable, refusing to execute: {e}") from e
        if not first_use:
            raise CapabilityError("capability already used")
    return claims


def _bearer(ctx: Context | None) -> str | None:
    """Test seam: tests replace this to inject a token without an HTTP request."""
    request = getattr(getattr(ctx, "request_context", None), "request", None) if ctx else None
    header = request.headers.get("authorization", "") if request is not None else ""
    return header[7:] if header.lower().startswith("bearer ") else None


def targets() -> dict[str, str]:
    pairs = [p.split("=", 1) for p in os.getenv("OPS_SERVICES", "").split(",") if "=" in p]
    return {name.strip(): url.strip().rstrip("/") for name, url in pairs}


def _execute(action: str, service: str, dry_run: bool, ctx: Context | None, path: str, body: dict | None = None) -> dict:
    base = targets().get(service)
    if not base:
        raise ValueError(f"unknown service {service!r}; known: {sorted(targets())}")
    claims = authorize(_bearer(ctx), action, service, dry_run)
    record = {"ts": datetime.now(timezone.utc).isoformat(), "audit": "remediation", "action": action,
              "service": service, "dry_run": dry_run, "incident_id": claims.get("incident_id"),
              "tenant_id": claims.get("tenant_id"), "approved_by": claims.get("approved_by"), "jti": claims["jti"]}
    if dry_run:
        result = {"plan": f"POST {path}", "body": body or {}}
    else:
        response = httpx.post(f"{base}{path}", json=body or {}, timeout=TIMEOUT)
        response.raise_for_status()
        result = response.json()
    log.info(json.dumps({**record, "result": result}))
    return {"executed": not dry_run, "dry_run": dry_run, "action": action, "service": service,
            "incident_id": claims.get("incident_id"), "result": result}


@mcp.tool()
def rollback(service: str, dry_run: bool = False, ctx: Context = None) -> dict:
    """Roll `service` back to its previous good build. Requires a capability for rollback on this service."""
    return _execute("rollback", service, dry_run, ctx, "/ops/rollback")


@mcp.tool()
def restart(service: str, dry_run: bool = False, ctx: Context = None) -> dict:
    """Rolling restart of `service`. Requires a capability for restart on this service."""
    return _execute("restart", service, dry_run, ctx, "/ops/restart")


@mcp.tool()
def increase_pool(service: str, size: int = 40, dry_run: bool = False, ctx: Context = None) -> dict:
    """Raise the DB connection pool of `service` to `size` (max 60 per the runbook). Requires a capability."""
    if not 1 <= size <= MAX_POOL:
        raise ValueError(f"pool size must be 1..{MAX_POOL} (runbook: never above {MAX_POOL} per pod)")
    return _execute("increase_pool", service, dry_run, ctx, "/ops/pool", {"size": size})


@mcp.tool()
def failover(service: str, dry_run: bool = False, ctx: Context = None) -> dict:
    """Fail `service` over to its degraded mode (e.g. cached prices). Requires a capability."""
    return _execute("failover", service, dry_run, ctx, "/ops/failover")


@mcp.tool()
def scale_out(service: str, replicas: int = 4, dry_run: bool = False, ctx: Context = None) -> dict:
    """Scale `service` out to `replicas` (max 10 without a capacity review). Requires a capability."""
    if not 1 <= replicas <= MAX_REPLICAS:
        raise ValueError(f"replicas must be 1..{MAX_REPLICAS} (larger scale-outs need a capacity review)")
    return _execute("scale_out", service, dry_run, ctx, "/ops/scale", {"replicas": replicas})


@mcp.custom_route("/healthz", methods=["GET"])
async def healthz(_: Request) -> JSONResponse:
    return JSONResponse({"status": "ok"})


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(mcp.streamable_http_app(), host=os.getenv("HOST", "127.0.0.1"), port=int(os.getenv("PORT", "8300")))
