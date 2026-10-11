"""Gateway → agent authentication. The agent doesn't trust the network.

Every call must carry a gateway-minted ES256 token (aud sentinel-agent, ~60 s), and the tenant in the
URL or body must be the tenant in the token: a compromised pod elsewhere in the cluster can't read
or decide another tenant's incidents, and even a valid token can't be replayed across tenants.
"""
from __future__ import annotations

import os

import jwt
from fastapi import HTTPException, Request
from starlette.concurrency import run_in_threadpool

from .gateway_keys import GatewayKeys
from .tenancy import CURRENT_TENANT

AUDIENCE = "sentinel-agent"
GATEWAY = GatewayKeys()


async def _tenant_of(request: Request) -> str | None:
    tenant = request.path_params.get("tenant_id")
    if tenant is None and request.method == "POST":
        try:
            tenant = (await request.json()).get("tenant_id")
        except ValueError:
            tenant = None
    return tenant


async def gateway_call(request: Request) -> dict:
    tenant = await _tenant_of(request)
    if os.getenv("SENTINEL_AGENT_AUTH", "required") == "disabled":
        CURRENT_TENANT.set(tenant)
        return {}
    header = request.headers.get("authorization", "")
    if not header.lower().startswith("bearer "):
        raise HTTPException(401, "gateway token required", headers={"WWW-Authenticate": "Bearer"})
    try:
        claims = await run_in_threadpool(GATEWAY.verify, header[7:], AUDIENCE)  # may fetch the JWKS
    except jwt.PyJWTError as e:
        raise HTTPException(401, f"invalid gateway token: {e}", headers={"WWW-Authenticate": "Bearer"}) from e
    if not tenant or claims.get("tenant_id") != tenant:
        raise HTTPException(403, "token is not valid for this tenant")
    CURRENT_TENANT.set(tenant)  # the checkpointer binds Postgres RLS to this (app/checkpoint.py)
    return claims
