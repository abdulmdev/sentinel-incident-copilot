"""One incident = one trace, continued from the caller, with node, tool and LLM spans."""
import pytest
from fastapi.testclient import TestClient
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

import app.main as main
from app import llm, telemetry
from app.graph import build_graph

from .fakes import FakeToolBox, ScriptedChatModel, tool_calls_reply

EXPORTER = InMemorySpanExporter()
telemetry.setup(main.app, exporter=EXPORTER)  # process-wide, like production startup

TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736"
INCIDENT = {"id": "inc-t", "tenant_id": "acme", "service": "checkout", "summary": "5xx", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}


@pytest.fixture
def client():
    EXPORTER.clear()
    main.app.state.graph = build_graph(FakeToolBox(), InMemorySaver())
    return TestClient(main.app)


def spans():
    return {s.name: s for s in EXPORTER.get_finished_spans()}


def test_incident_trace_continues_the_gateways_trace(client, monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    client.post("/incidents", json=INCIDENT,
                headers={"traceparent": f"00-{TRACE_ID}-00f067aa0ba902b7-01", "X-Sentinel-Tool-Token": "t"})

    finished = EXPORTER.get_finished_spans()
    assert {format(s.context.trace_id, "032x") for s in finished} == {TRACE_ID}  # one trace, end to end
    names = {s.name for s in finished}
    assert {"agent.investigator", "agent.tools", "agent.triage", "agent.rca"} <= names
    assert {"execute_tool get_recent_deploys", "execute_tool search_runbooks"} <= names
    rca = spans()["agent.rca"]
    assert rca.attributes["sentinel.tenant_id"] == "acme"
    tools = spans()["agent.tools"]
    assert tools.attributes["sentinel.budget.tool_calls"] >= 4


def test_llm_calls_carry_genai_attributes(client, monkeypatch):
    monkeypatch.setenv("SENTINEL_LLM_PROVIDER", "ollama")
    turns = iter([tool_calls_reply(("get_error_logs", {"service": "checkout", "level": "ERROR"}),
                                   usage={"input_tokens": 900, "output_tokens": 60, "total_tokens": 960}),
                  AIMessage(content="done", usage_metadata={"input_tokens": 5, "output_tokens": 5, "total_tokens": 10})])

    def factory(model, schema=None):
        if schema is None:
            return ScriptedChatModel(responder=lambda _: next(turns))
        return ScriptedChatModel(responder=lambda _: AIMessage(content="not json"))
    monkeypatch.setattr(llm, "chat_model", factory)
    client.post("/incidents", json=INCIDENT, headers={"X-Sentinel-Tool-Token": "t"})

    chats = [s for s in EXPORTER.get_finished_spans() if s.attributes.get("gen_ai.operation.name") == "chat"]
    first = next(s for s in chats if s.attributes.get("gen_ai.usage.input_tokens") == 900)
    assert first.attributes["gen_ai.system"] == "ollama"
    assert first.attributes["gen_ai.request.model"] == "gemma4:latest"
    assert first.attributes["sentinel.llm.cost_usd"] == 0.0
    assert first.parent is not None  # nested under the investigator node span


def test_guardrail_interventions_are_span_events(client, monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    box = FakeToolBox()
    box.fail = {"get_recent_deploys"}  # no deploy history -> policy blocks the rollback
    main.app.state.graph = build_graph(box, InMemorySaver())
    client.post("/incidents", json=INCIDENT, headers={"X-Sentinel-Tool-Token": "t"})
    events = [e.name for e in spans()["agent.rca"].events]
    assert "guardrail.action_overridden" in events
