"""Durability and isolation of checkpoints in Postgres. Needs Postgres:

  SENTINEL_TEST_PG_DSN=postgresql://sentinel_app:sentinel_app@localhost:5432/sentinel?options=-csearch_path%3Dlanggraph
"""
import os
import uuid

import psycopg
import pytest
from langgraph.types import Command

import app.graph as graph_mod
from app.checkpoint import open_checkpointer
from app.graph import build_graph, run_config
from app.tenancy import CURRENT_TENANT

from .fakes import FakeGuard, FakeToolBox

DSN = os.getenv("SENTINEL_TEST_PG_DSN")
pytestmark = pytest.mark.skipif(not DSN, reason="set SENTINEL_TEST_PG_DSN to run against Postgres")


def incident(incident_id, tenant="acme"):
    return {"id": incident_id, "tenant_id": tenant, "service": "checkout",
            "summary": "checkout 5xx rate above 20%", "severity": "SEV1", "opened_at": "2026-10-05T10:05:00Z"}


@pytest.fixture(autouse=True)
def fast(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.setattr(graph_mod, "VERIFY_INTERVAL_S", 0)


async def test_incident_survives_restart_and_resumes_on_another_process():
    incident_id = f"durable-{uuid.uuid4()}"
    guard = FakeGuard()  # outlives both "processes", as the real guard does
    CURRENT_TENANT.set("acme")  # what the authenticated request sets

    async with open_checkpointer(DSN) as saver:  # "process A" opens the incident, then dies
        await build_graph(FakeToolBox(), saver, guard).ainvoke({"incident": incident(incident_id)},
                                                         run_config("acme", incident_id, "t"))

    async with open_checkpointer(DSN) as saver:  # "process B": fresh pool, fresh graph
        g = build_graph(FakeToolBox(), saver, guard)
        before = (await g.aget_state(run_config("acme", incident_id))).values
        assert before["status"] == "AWAITING_APPROVAL"
        assert before["rca"]["remediation"]["action"] == "rollback"

        guard.approve()  # the approval lives in the guard, not in this process: it survives the restart too
        await g.ainvoke(Command(resume={"approved": True, "reviewer": "oncall@acme", "note": None}),
                        run_config("acme", incident_id, "t"))
        assert (await g.aget_state(run_config("acme", incident_id))).values["status"] == "RESOLVED"


async def test_row_level_security_isolates_checkpoints_by_the_requests_tenant():
    incident_id = f"rls-{uuid.uuid4()}"
    CURRENT_TENANT.set("acme")
    async with open_checkpointer(DSN) as saver:
        g = build_graph(FakeToolBox(), saver)
        await g.ainvoke({"incident": incident(incident_id)}, run_config("acme", incident_id, "t"))

        # A request authenticated for globex that (by a bug) addresses acme's thread sees nothing...
        CURRENT_TENANT.set("globex")
        assert not (await g.aget_state(run_config("acme", incident_id))).values
        # ...and can't write into it either (RLS WITH CHECK on insert).
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            await g.ainvoke({"incident": incident(f"{incident_id}-x")}, run_config("acme", f"{incident_id}-x", "t"))
        # No tenant bound at all (an unauthenticated code path): nothing visible.
        CURRENT_TENANT.set(None)
        assert not (await g.aget_state(run_config("acme", incident_id))).values

        CURRENT_TENANT.set("acme")
        assert (await g.aget_state(run_config("acme", incident_id))).values["status"] == "AWAITING_APPROVAL"


async def test_tables_force_rls_even_for_their_owner():
    async with open_checkpointer(DSN):
        pass
    async with await psycopg.AsyncConnection.connect(DSN) as conn:
        rows = await (await conn.execute(
            "SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class "
            "WHERE relname IN ('checkpoints', 'checkpoint_blobs', 'checkpoint_writes') "
            "AND relnamespace = current_schema()::regnamespace")).fetchall()
    assert sorted(rows) == [(t, True, True) for t in ("checkpoint_blobs", "checkpoint_writes", "checkpoints")]
