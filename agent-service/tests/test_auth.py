"""Gateway → agent calls are authenticated per request and bound to one tenant."""
import json
import time

import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric import ec
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import InMemorySaver

import app.auth as auth
import app.main as main
from app.gateway_keys import GatewayKeys
from app.graph import build_graph

from .fakes import FakeToolBox

pytestmark = pytest.mark.real_auth

ISSUER = "http://gateway.test"
KEY = ec.generate_private_key(ec.SECP256R1())
JWKS = {"keys": [{**json.loads(jwt.algorithms.ECAlgorithm.to_jwk(KEY.public_key())), "kid": "k1", "alg": "ES256"}]}
INCIDENT = {"id": "inc-a", "tenant_id": "acme", "service": "checkout", "summary": "5xx", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}


def token(tenant="acme", aud="sentinel-agent", key=KEY, ttl=60, alg="ES256"):
    now = int(time.time())
    claims = {"iss": ISSUER, "sub": "gateway", "aud": aud, "tenant_id": tenant, "iat": now, "exp": now + ttl}
    if alg == "HS256":
        return jwt.encode(claims, "dev-only-secret-change-me-0123456789abcdef", algorithm="HS256")
    return jwt.encode(claims, key, algorithm="ES256", headers={"kid": "k1"})


@pytest.fixture
def client(monkeypatch):
    monkeypatch.delenv("SENTINEL_AGENT_AUTH", raising=False)
    monkeypatch.setattr(auth, "GATEWAY", GatewayKeys(issuer=ISSUER, jwks=JWKS))
    main.app.state.graph = build_graph(FakeToolBox(), InMemorySaver())
    return TestClient(main.app)


def bearer(t):
    return {"Authorization": f"Bearer {t}"}


def test_calls_without_a_gateway_token_are_rejected(client):
    assert client.post("/incidents", json=INCIDENT).status_code == 401
    assert client.get("/tenants/acme/incidents/inc-a").status_code == 401


def test_gateway_token_for_the_tenant_is_accepted(client):
    assert client.post("/incidents", json=INCIDENT, headers=bearer(token())).json()["status"] == "AWAITING_APPROVAL"
    assert client.get("/tenants/acme/incidents/inc-a", headers=bearer(token())).status_code == 200


def test_token_for_another_tenant_is_forbidden_in_path_and_body(client):
    client.post("/incidents", json=INCIDENT, headers=bearer(token()))
    assert client.get("/tenants/acme/incidents/inc-a", headers=bearer(token("globex"))).status_code == 403
    assert client.post("/incidents", json={**INCIDENT, "id": "inc-b"}, headers=bearer(token("globex"))).status_code == 403
    r = client.post("/tenants/acme/incidents/inc-a/decision", json={"approved": True, "reviewer": "x"},
                    headers=bearer(token("globex")))
    assert r.status_code == 403


@pytest.mark.parametrize("bad", [
    pytest.param(dict(aud="sentinel-mcp"), id="tool-token-audience"),
    pytest.param(dict(key=ec.generate_private_key(ec.SECP256R1())), id="forged-key"),
    pytest.param(dict(ttl=-10), id="expired"),
    pytest.param(dict(alg="HS256"), id="shared-secret"),
])
def test_invalid_tokens_are_rejected(client, bad):
    assert client.post("/incidents", json=INCIDENT, headers=bearer(token(**bad))).status_code == 401


def test_health_and_metrics_stay_open(client):
    assert client.get("/healthz").status_code == 200
    assert client.get("/metrics").status_code == 200
