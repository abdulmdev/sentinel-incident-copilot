"""The LLM investigation loop, driven by a scripted model: budgets are enforced by the graph."""
import pytest
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver

from app import graph as graph_mod
from app import llm
from app.graph import build_graph, run_config

from .fakes import FakeToolBox, ScriptedChatModel, tool_calls_reply

INCIDENT = {"id": "inc-9", "tenant_id": "acme", "service": "checkout",
            "summary": "checkout 5xx rate above 20%", "severity": "SEV1",
            "opened_at": "2026-10-05T10:05:00Z"}
EVERYTHING = (("get_recent_deploys", {"service": "checkout"}),
              ("get_error_logs", {"service": "checkout", "level": "ERROR"}),
              ("get_service_metrics", {"service": "checkout"}))


@pytest.fixture(autouse=True)
def llm_on(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test-not-used")
    monkeypatch.setattr(graph_mod, "MAX_TOOL_CALLS", 8)
    monkeypatch.setattr(graph_mod, "MAX_COST_USD", 0.50)


def use_model(monkeypatch, responder):
    """Investigator gets `responder`; triage/RCA get unparseable text so they fall back to heuristics."""
    def factory(model, schema=None):
        if model == llm.model_for("investigate") and responder is not None:
            return ScriptedChatModel(responder=responder)
        return ScriptedChatModel(responder=lambda _: AIMessage(content="not json"))
    monkeypatch.setattr(llm, "chat_model", factory)


async def run(toolbox):
    g = build_graph(toolbox, InMemorySaver())
    cfg = run_config("acme", "inc-9", "tok")
    await g.ainvoke({"incident": INCIDENT}, cfg)
    return (await g.aget_state(cfg)).values


async def test_model_that_never_stops_is_cut_off_at_the_step_cap(monkeypatch):
    use_model(monkeypatch, lambda _: tool_calls_reply(*EVERYTHING))  # always wants 3 more calls
    toolbox = FakeToolBox()
    state = await run(toolbox)

    assert state["budget"]["stop_reason"] == "step_cap"
    assert state["budget"]["tool_calls"] == 8
    assert len(toolbox.calls) == 8  # 3 + 3 + 2; the 9th was answered "skipped", not executed
    assert state["status"] == "AWAITING_APPROVAL"  # capped investigations still produce an RCA
    assert state["rca"]["remediation"]["action"] == "rollback"


async def test_expensive_model_is_cut_off_at_the_cost_cap(monkeypatch):
    usage = {"input_tokens": 50_000, "output_tokens": 10_000, "total_tokens": 60_000}  # $0.40/turn on Opus
    use_model(monkeypatch, lambda _: tool_calls_reply(*EVERYTHING, usage=usage))
    toolbox = FakeToolBox()
    state = await run(toolbox)

    assert state["budget"]["stop_reason"] == "cost_cap"
    assert state["budget"]["cost_usd"] == pytest.approx(0.80)
    assert len(toolbox.calls) == 3  # second turn's calls never ran: budget checked before executing


async def test_model_that_finishes_stops_cleanly(monkeypatch):
    turns = iter([tool_calls_reply(*EVERYTHING),
                  tool_calls_reply(("search_runbooks", {"query": "NullPointerException PaymentClient", "limit": 2})),
                  AIMessage(content="Deploy 1.5.0 introduced an NPE in PaymentClient.charge.",
                            usage_metadata={"input_tokens": 10, "output_tokens": 10, "total_tokens": 20})])
    use_model(monkeypatch, lambda _: next(turns))
    state = await run(FakeToolBox())

    assert state["budget"]["stop_reason"] == "done"
    assert state["budget"]["tool_calls"] == 4
    assert any(e["source"] == "runbook:checkout-rollback" for e in state["evidence"])
    assert [c["step"] for c in state["llm_calls"]].count("investigate") == 3


async def test_hallucinated_tool_is_an_error_result_not_a_crash(monkeypatch):
    turns = iter([tool_calls_reply(("delete_production_database", {})),
                  AIMessage(content="done", usage_metadata={"input_tokens": 1, "output_tokens": 1, "total_tokens": 2})])
    use_model(monkeypatch, lambda _: next(turns))
    state = await run(FakeToolBox())
    tool_msgs = [m for m in state["messages"] if m.type == "tool"]
    assert tool_msgs[0].status == "error" and "unknown tool" in tool_msgs[0].content
    assert state["status"] == "AWAITING_APPROVAL"


async def test_llm_outage_degrades_to_scripted_investigation(monkeypatch):
    def broken(_):
        raise ConnectionError("api down")
    use_model(monkeypatch, broken)
    toolbox = FakeToolBox()
    state = await run(toolbox)
    assert len(toolbox.calls) == 5  # the scripted plan ran instead
    assert state["rca"]["remediation"]["action"] == "rollback"
