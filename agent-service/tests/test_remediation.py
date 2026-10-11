"""After approval: execute exactly the approved action with the capability, then verify recovery."""
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import InMemorySaver

import app.main as main
from app import graph as graph_mod
from app.graph import build_graph

from .fakes import BAD_DEPLOY, FakeToolBox

INCIDENT = {"id": "inc-r", "tenant_id": "acme", "service": "checkout", "summary": "checkout 5xx", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}
CAP = "capability-for-rollback-checkout"


@pytest.fixture(autouse=True)
def fast_verify(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    monkeypatch.setattr(graph_mod, "VERIFY_INTERVAL_S", 0)
    monkeypatch.setattr(graph_mod, "VERIFY_ATTEMPTS", 3)


def setup(box):
    main.app.state.graph = build_graph(box, InMemorySaver())
    client = TestClient(main.app)
    client.post("/incidents", json=INCIDENT, headers={"X-Sentinel-Tool-Token": "t"})
    return client


def decide(client, approved=True, capability=CAP):
    headers = {"X-Sentinel-Remediation-Token": capability} if capability else {}
    return client.post("/tenants/acme/incidents/inc-r/decision", headers=headers,
                       json={"approved": approved, "reviewer": "alice@acme"}).json()


def test_approved_rollback_executes_with_the_capability_and_verifies():
    box = FakeToolBox()
    body = decide(setup(box))

    assert box.capabilities == [CAP]
    assert ("rollback", {"service": "checkout"}) in box.calls
    assert body["execution"]["executed"] is True
    assert body["verification"]["healthy"] is True
    assert body["status"] == "RESOLVED"
    tl = body["timeline"]
    assert tl["proposed_at"] <= tl["decided_at"] <= tl["executed_at"] <= tl["verified_at"]
    assert tl["time_to_mitigate_s"] is not None
    assert "## Execution" in body["postmortem"] and "## Timeline" in body["postmortem"]
    assert "Time to proposal" in body["postmortem"] and "**Outcome:** RESOLVED" in body["postmortem"]


def test_no_capability_means_nothing_executes():
    box = FakeToolBox()
    body = decide(setup(box), capability=None)
    assert body["status"] == "REMEDIATION_FAILED"
    assert body["execution"]["executed"] is False
    assert not [c for c in box.calls if c[0] in {"rollback", "restart", "increase_pool", "failover"}]


def test_refused_capability_is_a_failure_not_a_success():
    body = decide(setup(FakeToolBox(refuse_capability=True)))
    assert body["status"] == "REMEDIATION_FAILED"
    assert "capability already used" in body["execution"]["error"]
    assert "verification" not in body  # nothing to verify


def test_fix_that_doesnt_heal_is_caught_by_verification():
    box = FakeToolBox(heals=False)
    body = decide(setup(box))
    assert body["execution"]["executed"] is True
    assert body["verification"]["healthy"] is False
    assert len(body["verification"]["attempts"]) == 3
    assert body["status"] == "REMEDIATION_FAILED"
    assert body["timeline"]["time_to_mitigate_s"] is None


def test_approved_escalation_executes_nothing():
    world = {**BAD_DEPLOY, "deploys": [], "ERROR": [{"message": "certificate has expired", "count": 9, "latest": {}}]}
    box = FakeToolBox(world=world)
    client = setup(box)
    assert client.get("/tenants/acme/incidents/inc-r").json()["rca"]["remediation"]["action"] == "escalate"
    body = decide(client)
    assert body["status"] == "ESCALATED"
    assert box.capabilities == []
    assert "escalated to a human" in body["postmortem"]


def test_rejection_executes_nothing():
    box = FakeToolBox()
    body = decide(setup(box), approved=False)
    assert body["status"] == "CLOSED_NO_ACTION" and box.capabilities == []


def test_capability_is_never_checkpointed():
    box = FakeToolBox()
    decide(setup(box))
    snapshot = main.app.state.graph.get_state({"configurable": {"thread_id": "acme:inc-r"}})
    assert CAP not in str(snapshot.values)


def test_action_the_adapter_cannot_perform_fails_clearly():
    box = FakeToolBox()
    real = box.remediation_tools

    async def without_rollback(capability):
        return [t for t in await real(capability) if t.name != "rollback"]

    box.remediation_tools = without_rollback
    body = decide(setup(box))
    assert body["status"] == "REMEDIATION_FAILED"
    assert "no 'rollback' operation" in body["execution"]["error"]
    assert "verification" not in body or not body["verification"]  # nothing ran, so nothing to verify
