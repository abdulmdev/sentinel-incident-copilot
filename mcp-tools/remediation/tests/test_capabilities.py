"""The remediation server must execute exactly what a human approved, once, and nothing else.

Capabilities come from Sentinel Guard; the adapter verifies them with the guard's public key and
redeems them at the guard (which enforces single use) before touching the service.
"""
import json
import time
import uuid

import httpx
import jwt
import pytest
import respx
from cryptography.hazmat.primitives.asymmetric import ec
from mcp.shared.memory import create_connected_server_and_client_session

import server
from gateway_keys import GatewayKeys

ISSUER = "sentinel-guard"
GUARD_URL = "http://guard.test"
BASE = "http://checkout:8000"
GUARD_KEY = ec.generate_private_key(ec.SECP256R1())   # what only the guard holds
DEV_SECRET = "dev-only-secret-change-me-0123456789abcdef"  # the old (Phase 7) shared secret


def jwks(private_key, kid="k1"):
    jwk = json.loads(jwt.algorithms.ECAlgorithm.to_jwk(private_key.public_key()))
    return {"keys": [{**jwk, "kid": kid, "alg": "ES256", "use": "sig"}]}


def capability(action="rollback", target="checkout", aud="sentinel-action", ttl=300, key=GUARD_KEY, iss=ISSUER,
               change=None, **extra):
    now = int(time.time())
    return jwt.encode({"iss": iss, "sub": "system:serviceaccount:sentinel:sentinel-agent", "aud": aud,
                       "request_id": "ar-1234abcd-1", "action": action, "target": target, "namespace": "acme",
                       "change": change if change is not None else {"incident": "inc-1"}, "approved_by": "alice@acme",
                       "jti": f"cap-{uuid.uuid4()}", "iat": now, "exp": now + ttl, **extra},
                      key, algorithm="ES256", headers={"kid": "k1"})


class FakeGuardSpend:
    """The guard's POST /v1/capabilities/spend: 200 the first time for a jti, 409 after."""

    def __init__(self):
        self.spent: set[str] = set()

    def __call__(self, request):
        claims = jwt.decode(json.loads(request.content)["capability"], options={"verify_signature": False})
        if claims["jti"] in self.spent:
            return httpx.Response(409, json={"error": "conflict: capability already used or no longer valid"})
        self.spent.add(claims["jti"])
        return httpx.Response(200, json={"request": {"id": claims["request_id"]}, "claims": claims})


@pytest.fixture(autouse=True)
def env(monkeypatch):
    monkeypatch.setenv("OPS_SERVICES", f"checkout={BASE}")
    monkeypatch.setenv("SENTINEL_GUARD_URL", GUARD_URL)
    monkeypatch.setattr(server, "GUARD", GatewayKeys(issuer=ISSUER, jwks=jwks(GUARD_KEY)))


@pytest.fixture
def token(monkeypatch):
    holder = {"value": None}
    monkeypatch.setattr(server, "_bearer", lambda ctx: holder["value"])
    return holder


@pytest.fixture
def guard():
    spend = FakeGuardSpend()
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(f"{GUARD_URL}/v1/capabilities/spend").mock(side_effect=spend)
        yield mock, route


async def call(tool, **args):
    async with create_connected_server_and_client_session(server.mcp._mcp_server) as client:
        return await client.call_tool(tool, args)


async def test_approved_action_executes_once(token, guard):
    mock, spend = guard
    token["value"] = capability()
    route = mock.post(f"{BASE}/ops/rollback").mock(return_value=httpx.Response(200, json={"fixed_active_fault": True}))
    first = await call("rollback", service="checkout")
    second = await call("rollback", service="checkout")
    assert not first.isError and '"executed": true' in first.content[0].text
    assert '"guard_request": "ar-1234abcd-1"' in first.content[0].text
    assert second.isError and "already used" in second.content[0].text
    assert route.call_count == 1  # the replayed call never reached the service
    assert spend.call_count == 2


@pytest.mark.parametrize("cap,tool,args,reason", [
    ({"action": "restart"}, "rollback", {"service": "checkout"}, "covers restart on checkout"),
    ({"target": "inventory"}, "rollback", {"service": "checkout"}, "not rollback on checkout"),
    ({"aud": "sentinel-mcp"}, "rollback", {"service": "checkout"}, "invalid capability token"),
    ({"aud": "sentinel-remediation"}, "rollback", {"service": "checkout"}, "invalid capability token"),
    ({"ttl": -5}, "rollback", {"service": "checkout"}, "invalid capability token"),
    ({"action": "increase_pool", "change": {"incident": "inc-1", "size": "40"}}, "increase_pool",
     {"service": "checkout", "size": 60}, "approves size=40, not size=60"),
    ({"action": "scale_out", "change": {"incident": "inc-1"}}, "scale_out", {"service": "checkout", "replicas": 8},
     "approves replicas=None, not replicas=8"),
])
async def test_anything_not_exactly_approved_is_refused(token, guard, cap, tool, args, reason):
    mock, spend = guard
    token["value"] = capability(**cap)
    route = mock.post(url__regex=rf"{BASE}/ops/.*")
    result = await call(tool, **args)
    assert result.isError and reason in result.content[0].text
    assert route.call_count == 0 and spend.call_count == 0  # refused before redeeming anything


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
    token["value"] = jwt.encode({"iss": ISSUER, "aud": "sentinel-action", "request_id": "x", "action": "rollback",
                                 "target": "checkout", "jti": "x", "iat": now, "exp": now + 60},
                                DEV_SECRET, algorithm="HS256")
    result = await call("rollback", service="checkout")
    assert result.isError and "only ES256" in result.content[0].text


async def test_capability_from_another_issuer_is_refused(token):
    """Including the gateway's own tokens: only the guard issues capabilities now."""
    token["value"] = capability(iss="http://localhost:8080")
    result = await call("rollback", service="checkout")
    assert result.isError and "invalid capability token" in result.content[0].text


async def test_dry_run_validates_without_redeeming_or_touching_the_service(token, guard):
    mock, spend = guard
    token["value"] = capability()
    route = mock.post(f"{BASE}/ops/rollback").mock(return_value=httpx.Response(200, json={}))
    plan = await call("rollback", service="checkout", dry_run=True)
    assert spend.call_count == 0
    real = await call("rollback", service="checkout")
    assert '"dry_run": true' in plan.content[0].text and '"plan": "POST /ops/rollback"' in plan.content[0].text
    assert not real.isError and route.call_count == 1  # the dry run didn't use up the capability


@pytest.mark.parametrize("failure", [httpx.ConnectError("guard unreachable"), httpx.Response(503, text="starting")])
async def test_guard_outage_fails_closed(token, failure):
    token["value"] = capability()
    with respx.mock(assert_all_called=False) as mock:
        if isinstance(failure, httpx.Response):
            mock.post(f"{GUARD_URL}/v1/capabilities/spend").mock(return_value=failure)
        else:
            mock.post(f"{GUARD_URL}/v1/capabilities/spend").mock(side_effect=failure)
        route = mock.post(f"{BASE}/ops/rollback")
        result = await call("rollback", service="checkout")
    assert result.isError and ("refusing to execute" in result.content[0].text or "refused" in result.content[0].text)
    assert route.call_count == 0


async def test_pool_size_is_bounded_by_the_runbook(token):
    token["value"] = capability(action="increase_pool", change={"incident": "inc-1", "size": "200"})
    result = await call("increase_pool", service="checkout", size=200)
    assert result.isError and "never above 60" in result.content[0].text


async def test_scale_out_is_bounded_and_needs_its_own_capability(token, guard):
    mock, _ = guard
    token["value"] = capability(action="scale_out", change={"incident": "inc-1", "replicas": "4"})
    route = mock.post(f"{BASE}/ops/scale").mock(return_value=httpx.Response(200, json={"replicas": 4}))
    too_big = await call("scale_out", service="checkout", replicas=50)
    ok = await call("scale_out", service="checkout", replicas=4)
    assert too_big.isError and "capacity review" in too_big.content[0].text
    assert not ok.isError and route.call_count == 1
