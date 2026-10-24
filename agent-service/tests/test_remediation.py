"""After approval: collect the single-use capability from Sentinel Guard, execute exactly the
approved action with it, then verify recovery."""
import pytest
from fastapi.testclient import TestClient
from langgraph.checkpoint.memory import InMemorySaver

import app.main as main
from app import graph as graph_mod
from app.graph import build_graph

from .fakes import BAD_DEPLOY, FakeGuard, FakeToolBox

INCIDENT = {"id": "inc-r", "tenant_id": "acme", "service": "checkout", "summary": "checkout 5xx", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}


@pytest.fixture(autouse=True)
def fast_verify(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    monkeypatch.setattr(graph_mod, "VERIFY_INTERVAL_S", 0)
    monkeypatch.setattr(graph_mod, "VERIFY_ATTEMPTS", 3)


def setup(box, guard=None):
    main.app.state.graph = build_graph(box, InMemorySaver(), guard if guard is not None else FakeGuard())
    client = TestClient(main.app)
    client.post("/incidents", json=INCIDENT, headers={"X-Sentinel-Tool-Token": "t"})
    return client


def decide(client, approved=True):
    return client.post("/tenants/acme/incidents/inc-r/decision",
                       json={"approved": approved, "reviewer": "alice@acme"}).json()


def approved_run(box, guard=None):
    guard = guard if guard is not None else FakeGuard()
    client = setup(box, guard)
    guard.approve()  # the gateway records the reviewer's approval in the guard, then resumes the agent
    return decide(client), guard


def test_the_proposal_is_registered_with_the_guard_once_before_approval():
    guard = FakeGuard()
    client = setup(FakeToolBox(), guard)
    view = client.get("/tenants/acme/incidents/inc-r").json()
    assert view["approval"]["request"] == "ar-test-1" and view["approval"]["verdict"] == "pending"
    assert guard.proposals == [{"id": "ar-test-1", "action": "rollback", "tenant": "acme", "service": "checkout",
                                "change": {"incident": "inc-r"}, "intent": guard.proposals[0]["intent"]}]
    guard.approve()
    decide(client)
    assert len(guard.proposals) == 1  # resuming the graph must not propose again


def test_approved_rollback_executes_with_the_guards_capability_and_verifies():
    box = FakeToolBox()
    body, guard = approved_run(box)

    assert len(box.capabilities) == 1 and guard.issued == {"ar-test-1"}
    assert body["execution"]["guard_request"] == "ar-test-1"
    assert body["execution"]["capability_jti"] == "cap-ar-test-1"
    assert ("rollback", {"service": "checkout"}) in box.calls
    assert body["execution"]["executed"] is True
    assert body["verification"]["healthy"] is True
    assert body["status"] == "RESOLVED"
    tl = body["timeline"]
    assert tl["proposed_at"] <= tl["decided_at"] <= tl["executed_at"] <= tl["verified_at"]
    assert tl["time_to_mitigate_s"] is not None
    assert "## Execution" in body["postmortem"] and "## Timeline" in body["postmortem"]
    assert "Time to proposal" in body["postmortem"] and "**Outcome:** RESOLVED" in body["postmortem"]


def test_without_an_approval_in_the_guard_nothing_executes():
    """The agent's own resume is not authority: only an approval recorded in the guard is."""
    box = FakeToolBox()
    body = decide(setup(box))  # resumed as approved, but nobody approved it in the guard
    assert body["status"] == "REMEDIATION_FAILED"
    assert body["execution"]["executed"] is False and "needs human approval" in body["execution"]["error"]
    assert box.capabilities == []
    assert not [c for c in box.calls if c[0] in {"rollback", "restart", "increase_pool", "failover"}]


def test_guard_down_fails_closed():
    box, guard = FakeToolBox(), FakeGuard()
    client = setup(box, guard)
    guard.approve()
    guard.down = True
    body = decide(client)
    assert body["status"] == "REMEDIATION_FAILED" and "refusing to execute" in body["execution"]["error"]
    assert box.capabilities == []


def test_guard_down_at_proposal_time_still_shows_the_proposal_but_nothing_can_run():
    box = FakeToolBox()
    client = setup(box, FakeGuard(down=True))
    view = client.get("/tenants/acme/incidents/inc-r").json()
    assert view["status"] == "AWAITING_APPROVAL" and view["approval"]["request"] is None
    assert "unreachable" in view["approval"]["error"]
    body = decide(client)
    assert body["status"] == "REMEDIATION_FAILED" and box.capabilities == []


def test_parameters_are_part_of_what_is_approved():
    world = {**BAD_DEPLOY, "deploys": [], "ERROR": [{"message": "timeout acquiring DB connection", "count": 40,
                                                     "latest": {}}]}
    guard = FakeGuard()
    setup(FakeToolBox(world=world), guard)
    assert guard.proposals[0]["action"] == "increase_pool"
    assert guard.proposals[0]["change"] == {"incident": "inc-r", "size": 40}


def test_refused_capability_is_a_failure_not_a_success():
    body, _ = approved_run(FakeToolBox(refuse_capability=True))
    assert body["status"] == "REMEDIATION_FAILED"
    assert "capability already used" in body["execution"]["error"]
    assert "verification" not in body  # nothing to verify


def test_fix_that_doesnt_heal_is_caught_by_verification():
    box = FakeToolBox(heals=False)
    body, _ = approved_run(box)
    assert body["execution"]["executed"] is True
    assert body["verification"]["healthy"] is False
    assert len(body["verification"]["attempts"]) == 3
    assert body["status"] == "REMEDIATION_FAILED"
    assert body["timeline"]["time_to_mitigate_s"] is None


def test_approved_escalation_executes_nothing():
    world = {**BAD_DEPLOY, "deploys": [], "ERROR": [{"message": "certificate has expired", "count": 9, "latest": {}}]}
    box, guard = FakeToolBox(world=world), FakeGuard()
    client = setup(box, guard)
    assert client.get("/tenants/acme/incidents/inc-r").json()["rca"]["remediation"]["action"] == "escalate"
    body = decide(client)
    assert body["status"] == "ESCALATED"
    assert box.capabilities == [] and guard.proposals == []  # nothing to execute, nothing to register
    assert "escalated to a human" in body["postmortem"]


def test_rejection_executes_nothing():
    box = FakeToolBox()
    body = decide(setup(box), approved=False)
    assert body["status"] == "CLOSED_NO_ACTION" and box.capabilities == []


def test_capability_is_never_checkpointed():
    box = FakeToolBox()
    approved_run(box)
    snapshot = main.app.state.graph.get_state({"configurable": {"thread_id": "acme:inc-r"}})
    assert box.capabilities[0] not in str(snapshot.values)


def test_action_the_adapter_cannot_perform_fails_clearly():
    box = FakeToolBox()
    real = box.remediation_tools

    async def without_rollback(capability):
        return [t for t in await real(capability) if t.name != "rollback"]

    box.remediation_tools = without_rollback
    body, _ = approved_run(box)
    assert body["status"] == "REMEDIATION_FAILED"
    assert "no 'rollback' operation" in body["execution"]["error"]
    assert "verification" not in body or not body["verification"]  # nothing ran, so nothing to verify
