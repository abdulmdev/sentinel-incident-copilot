"""Scripted (no-LLM) path end to end through the API, with MCP-shaped tools."""
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import InMemorySaver

import app.main as main
from app.graph import build_graph

from .fakes import BAD_DEPLOY, FakeGuard, FakeToolBox

TOKEN = "scoped-tool-token-abc"


@pytest.fixture
def toolbox():
    return FakeToolBox()


@pytest.fixture
def guard():
    return FakeGuard()


@pytest.fixture
def client(monkeypatch, toolbox, guard):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    main.app.state.graph = build_graph(toolbox, InMemorySaver(), guard)
    return TestClient(main.app)  # no lifespan: we inject the graph


def incident(id="inc-1", tenant="acme"):
    return {"id": id, "tenant_id": tenant, "service": "checkout",
            "summary": "checkout 5xx rate above 20%", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}  # 5 min after the fake deploy


def open_incident(client, token=TOKEN, **kw):
    return client.post("/incidents", json=incident(**kw), headers={"X-Sentinel-Tool-Token": token} if token else {})


def test_bad_deploy_investigation_uses_tools_and_proposes_rollback(client, toolbox):
    body = open_incident(client).json()

    assert body["status"] == "AWAITING_APPROVAL"
    assert [name for name, _ in toolbox.calls] == [
        "get_recent_deploys", "get_error_logs", "get_error_logs", "get_service_metrics", "search_runbooks"]
    assert body["budget"] == {"tool_calls": 5, "cost_usd": 0.0, "stop_reason": "done"}
    assert body["triage"]["category"] == "deploy"
    assert body["rca"]["remediation"]["action"] == "rollback"
    causes = {c["hypothesis"]: c["citations"] for c in body["rca"]["root_causes"]}
    assert causes["Recent deployment introduced a regression"] == ["deploys:checkout"]
    # Grounded: the postmortem names the exact exception seen in the logs.
    assert causes["Code defect in the newly deployed build"] == ["logs:checkout", "runbook:postmortem-2026-08-14-checkout-npe"]
    assert "messages" not in body  # raw transcript stays internal


def test_runbooks_are_cited_only_when_they_name_the_observed_signature(client, toolbox):
    """Top search hit ≠ evidence. checkout-rollback is returned but never mentions the NPE."""
    body = open_incident(client).json()
    cited = {c for cause in body["rca"]["root_causes"] for c in cause["citations"]}
    assert "runbook:checkout-rollback" not in cited


def test_runbook_query_is_built_from_observed_symptoms(client, toolbox):
    open_incident(client)
    query = dict(toolbox.calls)["search_runbooks"]["query"]
    assert query == "NullPointerException in PaymentClient.charge"  # symptoms, not alert boilerplate


def test_runbook_text_never_triggers_rules(client, toolbox):
    """Runbook mentions NPE, but if the telemetry doesn't, there must be no 'code defect' cause."""
    toolbox.world = {**BAD_DEPLOY, "deploys": [], "ERROR": [
        {"message": "timeout acquiring DB connection", "count": 12, "latest": {"pool_max": 20}}]}
    body = open_incident(client).json()
    hypotheses = [c["hypothesis"] for c in body["rca"]["root_causes"]]
    assert hypotheses[0] == "Database connection pool exhausted"
    assert not any("Code defect" in h for h in hypotheses)
    assert body["rca"]["remediation"]["action"] == "increase_pool"


def test_tool_token_reaches_tools_but_is_never_persisted(client, toolbox):
    body = open_incident(client).json()
    assert set(toolbox.tokens) == {TOKEN}
    assert TOKEN not in str(body)
    snapshot = main.app.state.graph.get_state({"configurable": {"thread_id": "acme:inc-1"}})
    assert TOKEN not in str(snapshot.values)


def test_without_token_investigation_skips_runbooks(client, toolbox):
    body = open_incident(client, token=None).json()
    assert "search_runbooks" not in [n for n, _ in toolbox.calls]
    assert body["rca"]["remediation"]["action"] == "rollback"


def test_failing_tool_is_recorded_and_investigation_continues(client, toolbox):
    toolbox.fail = {"get_recent_deploys"}
    body = open_incident(client).json()
    assert body["status"] == "AWAITING_APPROVAL"
    assert not any(e["source"].startswith("deploys:") for e in body["evidence"])
    assert body["rca"]["root_causes"][0]["cause"] == "code_defect"  # still found the NPE in logs
    # ...but with deploy history unavailable there's nothing verified to roll back to: escalate.
    assert body["rca"]["remediation"]["action"] == "escalate"
    override = next(e for e in body["guardrail_events"] if e["type"] == "action_overridden")
    assert override["from"] == "rollback" and "no deploy" in override["reason"]


def test_approval_resumes_graph_and_writes_postmortem(client, guard, monkeypatch):
    monkeypatch.setattr("app.graph.VERIFY_INTERVAL_S", 0)
    open_incident(client)
    guard.approve()
    body = client.post("/tenants/acme/incidents/inc-1/decision",
                       json={"approved": True, "reviewer": "oncall@acme"}).json()
    assert body["status"] == "RESOLVED"
    assert "`rollback` on `checkout`" in body["postmortem"]
    assert "approved by oncall@acme" in body["postmortem"]
    assert "5 tool calls" in body["postmortem"]


def test_rejection_closes_without_action(client):
    open_incident(client)
    r = client.post("/tenants/acme/incidents/inc-1/decision", json={"approved": False, "reviewer": "lead"})
    assert r.json()["status"] == "CLOSED_NO_ACTION"


def test_decision_is_not_replayable(client):
    open_incident(client)
    client.post("/tenants/acme/incidents/inc-1/decision", json={"approved": True, "reviewer": "a"})
    r = client.post("/tenants/acme/incidents/inc-1/decision", json={"approved": True, "reviewer": "a"})
    assert r.status_code == 409


def test_duplicate_open_is_rejected(client):
    open_incident(client)
    assert open_incident(client).status_code == 409


def test_tenants_cannot_see_each_others_incidents(client):
    open_incident(client, tenant="acme")
    assert client.get("/tenants/globex/incidents/inc-1").status_code == 404
    r = client.post("/tenants/globex/incidents/inc-1/decision", json={"approved": True, "reviewer": "x"})
    assert r.status_code == 404


def test_after_a_rollback_a_new_pool_incident_is_not_rolled_back_again(client, toolbox):
    """Regression (live run): the earlier rollback's deploy record made the next incident propose rollback."""
    toolbox.world = {**BAD_DEPLOY,
                     "deploys": [{"version": "1.5.0", "commit": "a1b2c3d", "ts": "2026-10-05T10:00:00+00:00",
                                  "change": "switch payment client to async pool"},
                                 {"version": "1.4.2", "commit": "rollback", "ts": "2026-10-05T10:03:00+00:00",
                                  "change": "rollback from 1.5.0"}],
                     "ERROR": [{"message": "timeout acquiring DB connection", "count": 40, "latest": {"pool_max": 20}}]}
    body = open_incident(client).json()
    assert body["rca"]["remediation"]["action"] == "increase_pool"
    assert body["triage"]["category"] != "deploy"
