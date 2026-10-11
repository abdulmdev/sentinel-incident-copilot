import json

import httpx
import pytest
import respx
from mcp.shared.memory import create_connected_server_and_client_session

import server

BASE = "http://checkout:8000"


@pytest.fixture(autouse=True)
def services(monkeypatch):
    monkeypatch.setenv("OBSERVED_SERVICES", f"checkout={BASE}")


async def call(tool: str, **args):
    async with create_connected_server_and_client_session(server.mcp._mcp_server) as client:
        result = await client.call_tool(tool, args)
        return result


def payload(result):
    # Wire contract: exactly one text block holding one JSON object (what agents parse).
    assert len(result.content) == 1, result.content
    return json.loads(result.content[0].text)


async def test_tools_are_discoverable_over_mcp():
    async with create_connected_server_and_client_session(server.mcp._mcp_server) as client:
        names = {t.name for t in (await client.list_tools()).tools}
    assert names == {"list_services", "get_recent_deploys", "get_error_logs", "get_service_metrics", "probe_service"}


async def test_error_logs_are_grouped_with_counts():
    rows = [{"ts": f"t{i}", "service": "checkout", "level": "ERROR", "msg": "NullPointerException in PaymentClient.charge",
             "version": "1.5.0"} for i in range(40)]
    rows.append({"ts": "t99", "service": "checkout", "level": "ERROR", "msg": "downstream failure", "version": "1.5.0"})
    with respx.mock:
        respx.get(f"{BASE}/chaos/logs").mock(return_value=httpx.Response(200, json=rows))
        out = payload(await call("get_error_logs", service="checkout"))["logs"]
    assert out[0]["message"] == "NullPointerException in PaymentClient.charge"
    assert out[0]["count"] == 40
    assert out[0]["latest"]["ts"] == "t39"
    assert out[1]["count"] == 1


async def test_metrics_are_parsed_without_label_noise():
    text = ('chaos_requests_total{service="checkout",status="error"} 41.0\n'
            'chaos_db_pool_in_use{service="checkout"} 20.0\n'
            '# HELP something else\npython_gc_objects_collected_total 3.0\n')
    with respx.mock:
        respx.get(f"{BASE}/metrics").mock(return_value=httpx.Response(200, text=text))
        out = payload(await call("get_service_metrics", service="checkout"))["metrics"]
    assert out == {'chaos_requests_total{status="error"}': 41.0, "chaos_db_pool_in_use": 20.0}


async def test_unknown_service_is_a_tool_error_not_a_crash():
    result = await call("get_recent_deploys", service="payments")
    assert result.isError
    assert "unknown service" in result.content[0].text


async def test_invalid_level_rejected():
    result = await call("get_error_logs", service="checkout", level="DEBUG")
    assert result.isError


async def test_single_deploy_is_still_one_unambiguous_object():
    one = [{"version": "1.5.0", "commit": "a1b2c3d", "ts": "t", "change": "c"}]
    with respx.mock:
        respx.get(f"{BASE}/chaos/deploys").mock(return_value=httpx.Response(200, json=one))
        out = payload(await call("get_recent_deploys", service="checkout"))
    assert out == {"service": "checkout", "deploys": one}


async def test_probe_reports_success_rate_and_latency():
    calls = iter([503] * 3 + [200] * 17)
    with respx.mock:
        respx.get(f"{BASE}/work").mock(side_effect=lambda _: httpx.Response(next(calls)))
        result = payload(await call("probe_service", service="checkout", requests=20))
    assert result["requests"] == 20 and result["ok"] == 17 and result["errors"] == 3
    assert result["success_rate"] == 0.85
    assert result["p95_ms"] >= 0


async def test_probe_counts_timeouts_as_failures_and_caps_requests():
    with respx.mock:
        respx.get(f"{BASE}/work").mock(side_effect=httpx.ReadTimeout("slow"))
        result = payload(await call("probe_service", service="checkout", requests=500))
    assert result["requests"] == 50 and result["ok"] == 0 and result["success_rate"] == 0.0


async def test_probe_rejects_unknown_services():
    result = await call("probe_service", service="payroll")
    assert result.isError


# --- transport auth (zero trust: network access alone doesn't grant telemetry) ---------------------

import time

import jwt
from cryptography.hazmat.primitives.asymmetric import ec
from starlette.testclient import TestClient

from gateway_keys import GatewayKeys

ISSUER = "http://gateway.test"
KEY = ec.generate_private_key(ec.SECP256R1())
JWKS = {"keys": [{**json.loads(jwt.algorithms.ECAlgorithm.to_jwk(KEY.public_key())), "kid": "k1", "alg": "ES256"}]}
LIST = {"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
MCP_HEADERS = {"accept": "application/json, text/event-stream", "content-type": "application/json"}


def tool_token(scope="knowledge:read telemetry:read", aud="sentinel-mcp", key=KEY):
    now = int(time.time())
    return jwt.encode({"iss": ISSUER, "aud": aud, "scope": scope, "tenant_id": "acme", "iat": now, "exp": now + 60},
                      key, algorithm="ES256", headers={"kid": "k1"})


@pytest.fixture(scope="module")
def client():
    # FastMCP's session manager can run once per process: one app, one client for all auth tests.
    app = server.RequireToolToken(server.mcp.streamable_http_app(), GatewayKeys(issuer=ISSUER, jwks=JWKS))
    with TestClient(app, base_url="http://127.0.0.1:8200") as c:  # FastMCP rejects unknown Host headers
        yield c


def http_status(client, token=None):
    headers = {**MCP_HEADERS, **({"authorization": f"Bearer {token}"} if token else {})}
    return client.post("/mcp", json=LIST, headers=headers).status_code


def test_mcp_requires_a_gateway_tool_token(client):
    assert http_status(client) == 401
    assert http_status(client, tool_token()) == 200


def test_wrong_audience_scope_or_key_is_rejected(client):
    assert http_status(client, tool_token(aud="sentinel-remediation")) == 401
    assert http_status(client, tool_token(scope="knowledge:read")) == 401          # no telemetry:read
    assert http_status(client, tool_token(key=ec.generate_private_key(ec.SECP256R1()))) == 401


def test_health_stays_open(client):
    assert client.get("/healthz").status_code == 200
