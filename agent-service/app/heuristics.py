"""Rule-based triage + RCA: runs offline, and is the eval baseline the LLM path must beat."""
from __future__ import annotations

import re
from datetime import datetime, timedelta

from .models import Evidence, Incident, RcaResult, Remediation, RootCause, Triage

# From the checkout rollback runbook: a deploy is a suspect if errors started within 30 minutes of it.
DEPLOY_SUSPECT_WINDOW = timedelta(minutes=30)
_DEPLOY_TS = re.compile(r" at (\S+) \(")

# (signature in evidence, triage category, cause class, hypothesis, remediation action)
RULES = [
    ("deployment completed", "deploy", "deploy_regression", "Recent deployment introduced a regression", "rollback"),
    ("NullPointerException", "deploy", "code_defect", "Code defect in the newly deployed build", "rollback"),
    ("timeout acquiring DB connection", "resource", "db_pool_exhaustion", "Database connection pool exhausted",
     "increase_pool"),
    ("GC pause exceeded", "resource", "memory_leak", "Memory leak causing long GC pauses", "restart"),
    ("upstream returned 503", "dependency", "dependency_failure", "Upstream dependency (pricing-api) is failing",
     "failover"),
    ("slow upstream call", "latency", "dependency_latency", "Upstream dependency latency regression", "failover"),
    ("downstream failure", "dependency", "downstream_failure", "Failure is propagating from a downstream service",
     "escalate"),
]


def _deploy_is_suspect(e: Evidence, opened_at: datetime) -> bool:
    """A deploy finished hours before the alert didn't cause it. Rolling it back would be harmful."""
    m = _DEPLOY_TS.search(e.detail)
    if not m:
        return True  # unknown timing: stay a suspect rather than silently drop it
    try:
        deployed = datetime.fromisoformat(m.group(1))
    except ValueError:
        return True
    return timedelta(0) <= opened_at - deployed <= DEPLOY_SUSPECT_WINDOW


def _observed(evidence: list[Evidence], incident: Incident) -> list[Evidence]:
    """Telemetry that can trigger a rule. Runbook text describes *possible* failures and never does;
    deploys only count inside the suspect window."""
    return [e for e in evidence if not e.source.startswith("runbook:")
            and (not e.source.startswith("deploys:") or _deploy_is_suspect(e, incident.opened_at))]


def triage(incident: Incident, evidence: list[Evidence]) -> Triage:
    blob = " ".join(e.detail for e in _observed(evidence, incident)) + " " + incident.summary
    for sig, category, _, _, _ in RULES:
        if sig in blob:
            return Triage(category=category, blast_radius=incident.service, rationale=f"matched '{sig}'")
    return Triage(category="unknown", blast_radius=incident.service, rationale="no known signature")


def rca(incident: Incident, evidence: list[Evidence]) -> RcaResult:
    causes: list[RootCause] = []
    action = "escalate"
    for sig, _, cause, hypothesis, rule_action in RULES:
        cites = sorted({e.source for e in _observed(evidence, incident) if sig in e.detail})
        if cites:
            # Ground the cause in a runbook only if that runbook names the same signature.
            runbooks = sorted({e.source for e in evidence if e.source.startswith("runbook:") and sig in e.detail})
            causes.append(RootCause(cause=cause, hypothesis=hypothesis, confidence=0.6, citations=cites + runbooks))
            if action == "escalate":
                action = rule_action
    if not causes:
        causes.append(RootCause(cause="unknown", hypothesis="Insufficient evidence; needs human investigation",
                                confidence=0.1, citations=[]))
    risk = "low" if action in {"rollback", "scale_out"} else "medium"
    return RcaResult(root_causes=causes,
                     remediation=Remediation(action=action, target=incident.service,
                                             reason=causes[0].hypothesis, risk=risk))
