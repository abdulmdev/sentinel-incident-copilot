"""The remediation server must execute exactly what a human approved, once, and nothing else."""
import time
import uuid

import httpx
import jwt
import pytest
import respx
from mcp.shared.memory import create_connected_server_and_client_session

import json

from cryptography.hazmat.primitives.asymmetric import ec

import server
from gateway_keys import GatewayKeys

ISSUER = "http://gateway.test"
BASE = "http://checkout:8000"
GATEWAY_KEY = ec.generate_private_key(ec.SECP256R1())   # what only the gateway holds
DEV_SECRET = "dev-only-secret-change-me-0123456789abcdef"  # the old (Phase 7) shared secret


def jwks(private_key, kid="k1"):
    jwk = json.loads(jwt.algorithms.ECAlgorithm.to_jwk(private_key.public_key()))
    return {"keys": [{**jwk, "kid": kid, "alg": "ES256", "use": "sig"}]}


def capability(action="rollback", target="checkout", aud="sentinel-remediation", scope="remediation:execute",
               ttl=300, key=GATEWAY_KEY, iss=ISSUER, **extra):
    now = int(time.time())
    return jwt.encode({"iss": iss, "sub": "agent:inc-1", "aud": aud, "scope": scope, "tenant_id": "acme",
                       "incident_id": "inc-1", "action": action, "target": target, "approved_by": "alice@acme",
                       "jti": str(uuid.uuid4()), "iat": now, "exp": now + ttl, **extra},
                      key, algorithm="ES256", headers={"kid": "k1"})


@pytest.fixture(autouse=True)
def env(monkeypatch):
    monkeypatch.setenv("OPS_SERVICES", f"checkout={BASE}")
    monkeypatch.setattr(server, "STORE", server.MemoryReplayStore())
    monkeypatch.setattr(server, "GATEWAY", GatewayKeys(issuer=ISSUER, jwks=jwks(GATEWAY_KEY)))


@pytest.fixture
def token(monkeypatch):
    holder = {"value": None}
    monkeypatch.setattr(server, "_bearer", lambda ctx: holder["value"])
    return holder


async def call(tool, **args):
    async with create_connected_server_and_client_session(server.mcp._mcp_server) as client:
        return await client.call_tool(tool, args)


async def test_approved_action_executes_once(token):
    token["value"] = capability()
    with respx.mock:
        route = respx.post(f"{BASE}/ops/rollback").mock(return_value=httpx.Response(200, json={"fixed_active_fault": True}))
        first = await call("rollback", service="checkout")
        second = await call("rollback", service="checkout")
    assert not first.isError and '"executed": true' in first.content[0].text
    assert second.isError and "already used" in second.content[0].text
    assert route.call_count == 1  # the replayed call never reached the service


@pytest.mark.parametrize("cap,tool,args,reason", [
    ({"action": "restart"}, "rollback", {"service": "checkout"}, "covers restart on checkout"),
    ({"target": "inventory"}, "rollback", {"service": "checkout"}, "not rollback on checkout"),
    ({"aud": "sentinel-mcp"}, "rollback", {"service": "checkout"}, "invalid capability token"),
    ({"scope": "knowledge:read"}, "rollback", {"service": "checkout"}, "lacks remediation:execute"),
    ({"ttl": -5}, "rollback", {"service": "checkout"}, "invalid capability token"),
])
async def test_anything_not_exactly_approved_is_refused(token, cap, tool, args, reason):
    token["value"] = capability(**cap)
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(f"{BASE}/ops/rollback")
        result = await call(tool, **args)
    assert result.isError and reason in result.content[0].text
    assert route.call_count == 0


async def test_missing_token_is_refused(token):
    result = await call("restart", service="checkout")
    assert result.isError and "missing capability token" in result.content[0].text


async def test_forged_signature_is_refused(token):
    # Same kid, same claims, an attacker's EC key.
    token["value"] = capability(key=ec.generate_private_key(ec.SECP256R1()))
    result = await call("rollback", service="checkout")
    assert result.isError and "Signature verification failed" in result.content[0].text


async def test_capability_forged_with_the_old_shared_secret_is_refused(token):
    """Phase 7's gap: anything holding SENTINEL_JWT_SECRET could mint a capability. Not any more."""
    now = int(time.time())
    token["value"] = jwt.encode({"iss": ISSUER, "aud": "sentinel-remediation", "scope": "remediation:execute",
                                 "action": "rollback", "target": "checkout", "jti": "x", "iat": now, "exp": now + 60},
                                DEV_SECRET, algorithm="HS256")
    result = await call("rollback", service="checkout")
    assert result.isError and "only ES256" in result.content[0].text


async def test_capability_from_another_issuer_is_refused(token):
    token["value"] = capability(iss="http://evil.example")
    result = await call("rollback", service="checkout")
    assert result.isError and "invalid capability token" in result.content[0].text


async def test_dry_run_validates_without_consuming_or_touching_the_service(token):
    token["value"] = capability()
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(f"{BASE}/ops/rollback").mock(return_value=httpx.Response(200, json={}))
        plan = await call("rollback", service="checkout", dry_run=True)
        real = await call("rollback", service="checkout")
    assert '"dry_run": true' in plan.content[0].text and '"plan": "POST /ops/rollback"' in plan.content[0].text
    assert not real.isError and route.call_count == 1  # the dry run didn't use up the capability


async def test_replay_store_outage_fails_closed(token, monkeypatch):
    class Down:
        def claim(self, jti, ttl):
            raise ConnectionError("redis unreachable")
    monkeypatch.setattr(server, "STORE", Down())
    token["value"] = capability()
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(f"{BASE}/ops/rollback")
        result = await call("rollback", service="checkout")
    assert result.isError and "refusing to execute" in result.content[0].text
    assert route.call_count == 0


async def test_pool_size_is_bounded_by_the_runbook(token):
    token["value"] = capability(action="increase_pool")
    result = await call("increase_pool", service="checkout", size=200)
    assert result.isError and "never above 60" in result.content[0].text


async def test_scale_out_is_bounded_and_needs_its_own_capability(token):
    token["value"] = capability(action="scale_out")
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(f"{BASE}/ops/scale").mock(return_value=httpx.Response(200, json={"replicas": 4}))
        too_big = await call("scale_out", service="checkout", replicas=50)
        ok = await call("scale_out", service="checkout", replicas=4)
    assert too_big.isError and "capacity review" in too_big.content[0].text
    assert not ok.isError and route.call_count == 1
