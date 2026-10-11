from __future__ import annotations

from contextlib import asynccontextmanager
from typing import Annotated

from fastapi import Depends, FastAPI, Header, HTTPException, Request
from fastapi.responses import PlainTextResponse
from langchain_core.messages import BaseMessage
from langgraph.types import Command
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest

from .checkpoint import open_checkpointer
from .graph import build_graph, run_config
from . import telemetry
from .auth import gateway_call
from .models import Decision, Incident
from .toolbox import McpToolBox

RUNS = Counter("sentinel_agent_runs_total", "Incident graph runs", ["status"])
TOOL_CALLS = Histogram("sentinel_agent_tool_calls", "Tool calls per investigation", buckets=(1, 2, 4, 6, 8, 12))
STOPS = Counter("sentinel_agent_investigation_stops_total", "Why investigations ended", ["reason"])
LLM_TOKENS = Counter("sentinel_agent_llm_tokens_total", "LLM tokens", ["model", "step", "direction"])
LLM_COST = Counter("sentinel_agent_llm_cost_usd_total", "LLM spend in USD", ["model", "step"])
GUARDRAILS = Counter("sentinel_agent_guardrail_events_total", "Guardrail interventions", ["type"])
INCIDENT_SECONDS = Histogram("sentinel_agent_incident_seconds", "Time from open to proposed remediation",
                             buckets=(0.5, 1, 2, 5, 10, 30, 60, 120, 300))
REMEDIATIONS = Counter("sentinel_agent_remediations_total", "Remediation outcomes after a decision", ["action", "outcome"])
TIME_TO_MITIGATE = Histogram("sentinel_agent_time_to_mitigate_seconds", "Alert opened → verified healthy",
                             buckets=(30, 60, 120, 300, 600, 1200, 1800, 3600, 7200))


def _record(view: dict) -> None:
    for call in view.get("llm_calls", []):
        model, step = call.get("model", "?"), call.get("step", "?")
        for direction in ("input", "output"):
            if call.get(f"{direction}_tokens"):
                LLM_TOKENS.labels(model, step, direction).inc(call[f"{direction}_tokens"])
        LLM_COST.labels(model, step).inc(call.get("cost_usd") or 0)
    for event in view.get("guardrail_events", []):
        GUARDRAILS.labels(event["type"]).inc()


@asynccontextmanager
async def lifespan(app: FastAPI):
    async with open_checkpointer() as saver:
        app.state.graph = build_graph(McpToolBox(), saver)
        yield


app = FastAPI(title="sentinel-agent", lifespan=lifespan)
telemetry.setup(app)  # no-op unless OTEL_EXPORTER_OTLP_ENDPOINT is set


async def _view(request: Request, tenant_id: str, incident_id: str) -> dict:
    snap = await request.app.state.graph.aget_state(run_config(tenant_id, incident_id))
    if not snap.values:
        raise HTTPException(404, "incident not found")
    # The raw investigator transcript stays internal; callers get the evidence and conclusions.
    return {k: v for k, v in snap.values.items() if not isinstance(v, list) or not v or not isinstance(v[0], BaseMessage)}


@app.post("/incidents", dependencies=[Depends(gateway_call)])
async def open_incident(request: Request, incident: Incident,
                        x_sentinel_tool_token: Annotated[str | None, Header()] = None):
    graph = request.app.state.graph
    cfg = run_config(incident.tenant_id, incident.id, x_sentinel_tool_token)
    if (await graph.aget_state(cfg)).values:
        raise HTTPException(409, "incident already exists")
    with INCIDENT_SECONDS.time():
        await graph.ainvoke({"incident": incident.model_dump()}, cfg)  # runs until the approval interrupt
    view = await _view(request, incident.tenant_id, incident.id)
    _record(view)
    RUNS.labels(view["status"]).inc()
    TOOL_CALLS.observe(view["budget"]["tool_calls"])
    STOPS.labels(view["budget"]["stop_reason"] or "none").inc()
    return view


@app.get("/tenants/{tenant_id}/incidents/{incident_id}", dependencies=[Depends(gateway_call)])
async def get_incident(request: Request, tenant_id: str, incident_id: str):
    return await _view(request, tenant_id, incident_id)


@app.post("/tenants/{tenant_id}/incidents/{incident_id}/decision", dependencies=[Depends(gateway_call)])
async def decide(request: Request, tenant_id: str, incident_id: str, decision: Decision,
                 x_sentinel_remediation_token: Annotated[str | None, Header()] = None,
                 x_sentinel_tool_token: Annotated[str | None, Header()] = None):
    view = await _view(request, tenant_id, incident_id)
    if view.get("status") != "AWAITING_APPROVAL":
        raise HTTPException(409, f"incident is {view.get('status')}, not awaiting approval")
    # Credentials minted by the gateway go into config only, never checkpointed: the capability for
    # execution, the tool token for verification probes.
    cfg = run_config(tenant_id, incident_id, tool_token=x_sentinel_tool_token,
                     remediation_token=x_sentinel_remediation_token)
    await request.app.state.graph.ainvoke(Command(resume=decision.model_dump()), cfg)
    view = await _view(request, tenant_id, incident_id)
    RUNS.labels(view["status"]).inc()
    if decision.approved:
        REMEDIATIONS.labels(view["rca"]["remediation"]["action"], view["status"]).inc()
        ttm = (view.get("timeline") or {}).get("time_to_mitigate_s")
        if ttm is not None:
            TIME_TO_MITIGATE.observe(ttm)
    return view


@app.get("/metrics")
def metrics():
    return PlainTextResponse(generate_latest(), media_type=CONTENT_TYPE_LATEST)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}
