"""Remediation MCP server: the only component that changes production.

Every tool call must carry a **capability** issued by Sentinel Guard once a human approved the
incident's proposal (the gateway records the decision in the guard on the reviewer's behalf). It is
bound to one action on one target service, with its exact parameters, for one incident; it expires
with the approval and is **single-use**. So even a compromised agent can only execute exactly what
a human approved, once.

    Authorization: Bearer <capability>
    claims: iss=sentinel-guard, aud=sentinel-action, action, target, namespace (tenant),
            change {incident, size | replicas}, request_id, approved_by, jti, exp

Before any side effect the adapter verifies the capability against the guard's public key (ES256),
checks action, target and every parameter, then **redeems it at the guard**, which consumes it
atomically. If the guard can't be reached, nothing executes (fail closed): a duplicated rollback is
worse than a delayed one. Dry runs verify without redeeming or touching the service.

In production the same tool contracts would front the real deploy system (Argo Rollouts, kubectl,
the feature-flag service); here they drive chaos-lab's /ops endpoints.
"""
from __future__ import annotations

import json
import logging
import os
from datetime import datetime, timezone

import httpx
import jwt

from gateway_keys import GatewayKeys
from mcp.server.fastmcp import Context, FastMCP
from starlette.requests import Request
from starlette.responses import JSONResponse

AUDIENCE = "sentinel-action"
MAX_POOL = 60
MAX_REPLICAS = 10  # db-connection-pool runbook: never above 60 per pod
TIMEOUT = httpx.Timeout(10.0)
log = logging.getLogger("sentinel.remediation")
logging.basicConfig(level=logging.INFO, format="%(message)s")

mcp = FastMCP(
    "sentinel-remediation",
    instructions="Executes approved remediations. Every call needs a capability issued by Sentinel Guard.",
    host=os.getenv("HOST", "127.0.0.1"),
    port=int(os.getenv("PORT", "8300")),
    stateless_http=True,
    json_response=True,
)


class CapabilityError(PermissionError):
    pass


def guard_url() -> str:
    return os.getenv("SENTINEL_GUARD_URL", "http://localhost:8400").rstrip("/")


# The guard's public keys: this adapter can verify capabilities but never mint one.
GUARD = GatewayKeys(issuer=os.getenv("SENTINEL_GUARD_ISSUER", "sentinel-guard"),
                    jwks_url=f"{guard_url()}/.well-known/jwks.json")


def redeem(token: str) -> None:
    """Consumes the capability at the guard (atomically, once). Any doubt refuses."""
    try:
        response = httpx.post(f"{guard_url()}/v1/capabilities/spend", timeout=TIMEOUT,
                              json={"capability": token, "executor": "sentinel-remediation"})
    except httpx.HTTPError as e:
        raise CapabilityError(f"Sentinel Guard unavailable, refusing to execute: {e}") from e
    if response.status_code == 409:
        raise CapabilityError("capability already used")
    if response.status_code != 200:
        raise CapabilityError(f"Sentinel Guard refused the capability: HTTP {response.status_code} {response.text[:200]}")


# --- capability check ----------------------------------------------------------------------------

def authorize(token: str | None, action: str, service: str, dry_run: bool, params: dict | None = None) -> dict:
    """Validates the capability for exactly this action, service and parameters; redeems it unless dry run."""
    if not token:
        raise CapabilityError("missing capability token")
    try:
        claims = GUARD.verify(token, AUDIENCE)  # ES256, the guard's issuer, aud, exp
    except jwt.PyJWTError as e:
        raise CapabilityError(f"invalid capability token: {e}") from e
    if not claims.get("jti") or not claims.get("request_id"):
        raise CapabilityError("invalid capability token: no jti or request_id")
    if claims.get("action") != action or claims.get("target") != service:
        raise CapabilityError(f"capability covers {claims.get('action')} on {claims.get('target')}, "
                              f"not {action} on {service}")
    approved = claims.get("change") or {}
    for name, value in (params or {}).items():
        if str(approved.get(name)) != str(value):
            raise CapabilityError(f"capability approves {name}={approved.get(name)}, not {name}={value}")
    if not dry_run:
        redeem(token)
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
    claims = authorize(_bearer(ctx), action, service, dry_run, body)
    incident = (claims.get("change") or {}).get("incident")
    record = {"ts": datetime.now(timezone.utc).isoformat(), "audit": "remediation", "action": action,
              "service": service, "dry_run": dry_run, "incident_id": incident, "tenant_id": claims.get("namespace"),
              "approved_by": claims.get("approved_by"), "guard_request": claims.get("request_id"), "jti": claims["jti"]}
    if dry_run:
        result = {"plan": f"POST {path}", "body": body or {}}
    else:
        response = httpx.post(f"{base}{path}", json=body or {}, timeout=TIMEOUT)
        response.raise_for_status()
        result = response.json()
    log.info(json.dumps({**record, "result": result}))
    return {"executed": not dry_run, "dry_run": dry_run, "action": action, "service": service,
            "incident_id": incident, "guard_request": claims.get("request_id"), "result": result}


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
