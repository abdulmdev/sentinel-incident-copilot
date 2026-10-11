"""Turns raw tool output into citable evidence lines."""
from __future__ import annotations

import json

from .models import Evidence

MAX_DETAIL = 1500


def _loads(text: str):
    try:
        return json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return None


def _field(data, key: str):
    return data.get(key) if isinstance(data, dict) else None


def _is_rollback(d: dict) -> bool:
    return d.get("commit") == "rollback" or str(d.get("change", "")).startswith("rollback")


def _deploy_details(deploys: list[dict]) -> list[str]:
    """Only a deploy that is still running can be a suspect. Builds before a later rollback are gone, and
    the rollback itself restores the known-good baseline: neither is phrased as a completed deployment,
    so neither can trigger the deploy rule or satisfy the rollback interlock (no rolling back a rollback)."""
    last_rollback = max((i for i, d in enumerate(deploys) if _is_rollback(d)), default=-1)
    details = []
    for i, d in enumerate(deploys):
        when = f"at {d.get('ts')} ({d.get('change')})"
        if _is_rollback(d):
            details.append(f"rollback completed: restored version {d.get('version')} {when}")
        elif i < last_rollback:
            details.append(f"deployment rolled back, no longer running: version {d.get('version')} "
                           f"commit {d.get('commit')} {when}")
        else:
            details.append(f"deployment completed: version {d.get('version')} commit {d.get('commit')} {when}")
    return details


def from_tool(name: str, args: dict, text: str) -> list[Evidence]:
    """Observability tools return {"deploys"|"logs"|"metrics": ...}; search_runbooks returns a list of hits."""
    raw = _loads(text)
    service = args.get("service", "")
    data = _field(raw, {"get_recent_deploys": "deploys", "get_error_logs": "logs",
                        "get_service_metrics": "metrics"}.get(name, "")) if isinstance(raw, dict) else raw
    if name == "get_recent_deploys" and isinstance(data, list):
        if not data:
            return [Evidence(source=f"deploys:{service}", detail="no recent deployments")]
        return [Evidence(source=f"deploys:{service}", detail=d) for d in _deploy_details(data)]
    if name == "get_error_logs" and isinstance(data, list):
        level = args.get("level", "ERROR").upper()
        return [Evidence(source=f"logs:{service}",
                         detail=f"{level} x{row.get('count')}: {row.get('message')} {row.get('latest', {})}"[:MAX_DETAIL])
                for row in data]
    if name == "get_service_metrics" and isinstance(data, dict):
        return [Evidence(source=f"metrics:{service}", detail="; ".join(f"{k}={v}" for k, v in data.items()))]
    if name == "search_runbooks" and isinstance(data, list):
        return [Evidence(source=f"runbook:{h.get('docId')}",
                         detail=f"{h.get('heading')}: {h.get('text')}"[:MAX_DETAIL]) for h in data]
    label = service or args.get("query", "")
    return [Evidence(source=f"{name}:{label}", detail=text[:MAX_DETAIL])]
