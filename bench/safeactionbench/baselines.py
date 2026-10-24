"""Baselines that bracket the space, and a reference agent.

- always_escalate: never harmful, never useful. The floor for "safe".
- always_restart / always_rollback: the 3 a.m. reflexes. The ceiling for "harmful".
- symptom_agent: investigates through the same telemetry tools any agent gets (and the same outages),
  then matches symptoms to fixes. It is deliberately simple; it exists to show the harness end to end
  and to give a non-LLM reference point.
"""
from __future__ import annotations

from datetime import datetime, timedelta

from .dataset import load, public_incident
from .telemetry import Telemetry, ToolUnavailable


def always(action: str):
    def run(scenario: dict) -> dict:
        return {"id": scenario["id"], "action": action, "target": scenario["incident"]["service"]}
    return run


def symptom_agent(incident: dict, tools) -> dict:
    """Decides from what the tools return. `tools` has the Telemetry methods (direct or over MCP)."""
    service = incident["service"]

    def call(fn, *args):
        try:
            return fn(*args)
        except ToolUnavailable:
            return None

    deploys = (call(tools.get_recent_deploys, service) or {}).get("deploys", [])
    errors = call(tools.get_error_logs, service, "ERROR")
    warns = call(tools.get_error_logs, service, "WARN")
    metrics = (call(tools.get_service_metrics, service) or {}).get("metrics", {})
    logs_down = errors is None or warns is None
    lines = [l["message"] for l in ((errors or {}).get("logs", []) + (warns or {}).get("logs", []))]

    def seen(text: str) -> bool:
        return any(text in l for l in lines)

    opened = datetime.fromisoformat(incident["opened_at"])
    recent = any(timedelta(0) <= opened - datetime.fromisoformat(d["ts"]) <= timedelta(minutes=30) for d in deploys)
    decide = lambda a, t=service: {"id": incident["incident_id"], "action": a, "target": t}  # noqa: E731

    if seen("timeout acquiring DB connection") or metrics.get("chaos_db_pool_in_use", 0) >= 20:
        return decide("increase_pool")
    if seen("GC pause exceeded") or metrics.get("chaos_memory_mb", 0) >= 300:
        return decide("restart")
    if seen("upstream returned 503") or seen("slow upstream call"):
        return decide("failover")
    if seen("downstream failure"):  # the cause is in a dependency: look there
        services = (call(tools.list_services) or {}).get("services", [])
        for other in (s for s in services if s != service):
            m = (call(tools.get_service_metrics, other) or {}).get("metrics", {})
            if m.get("chaos_db_pool_in_use", 0) >= 20:
                return decide("increase_pool", other)
        return decide("escalate")
    if recent and not logs_down and lines:
        return decide("rollback")
    return decide("escalate")  # nothing it can stand behind: hand it to a human


BASELINES = {
    "always-escalate": always("escalate"),
    "always-restart": always("restart"),
    "always-rollback": always("rollback"),
    "symptom-agent": lambda s: symptom_agent(public_incident(s), Telemetry(s)),
}


def run(name: str, split: str = "dev") -> list[dict]:
    if name not in BASELINES:
        raise ValueError(f"unknown baseline {name!r}; one of {sorted(BASELINES)}")
    return [BASELINES[name](s) for s in load(split)]
