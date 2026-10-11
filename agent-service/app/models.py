from __future__ import annotations

import operator
from datetime import datetime, timezone
from typing import Annotated, Literal, Optional, TypedDict

from langchain_core.messages import AnyMessage
from langgraph.graph.message import add_messages
from pydantic import BaseModel, Field

Severity = Literal["SEV1", "SEV2", "SEV3"]

# Machine-checkable root-cause classes: the eval scores these, not free-text hypotheses.
CauseClass = Literal["deploy_regression", "code_defect", "db_pool_exhaustion", "memory_leak",
                     "dependency_failure", "dependency_latency", "downstream_failure", "unknown"]


class Incident(BaseModel):
    id: str
    tenant_id: str
    service: str
    summary: str
    severity: Severity = "SEV2"
    # When the alert fired. Deploy timing is judged against this, not against the agent's clock.
    opened_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))


class Evidence(BaseModel):
    source: str = Field(description="Where it came from, e.g. logs:checkout, deploys:checkout, runbook:db-connection-pool")
    detail: str


class Triage(BaseModel):
    category: Literal["latency", "errors", "resource", "deploy", "dependency", "unknown"]
    blast_radius: str
    rationale: str


class RootCause(BaseModel):
    cause: CauseClass = Field(description="Closest root-cause class; 'unknown' if none fits")
    hypothesis: str
    confidence: float = Field(ge=0.0, le=1.0)
    citations: list[str] = Field(description="Evidence sources supporting the hypothesis")


class Remediation(BaseModel):
    action: Literal["rollback", "restart", "scale_out", "increase_pool", "failover", "escalate"]
    target: str
    reason: str
    risk: Literal["low", "medium", "high"]


class RcaResult(BaseModel):
    root_causes: list[RootCause]
    remediation: Remediation


class Budget(TypedDict):
    tool_calls: int
    cost_usd: float
    stop_reason: Optional[str]  # done | step_cap | cost_cap | error


class IncidentState(TypedDict, total=False):
    incident: dict
    messages: Annotated[list[AnyMessage], add_messages]  # investigator ⇄ tools transcript
    evidence: Annotated[list[dict], operator.add]
    budget: Budget
    triage: dict
    rca: dict
    decision: dict
    status: str
    postmortem: str
    llm_calls: Annotated[list[dict], operator.add]
    guardrail_events: Annotated[list[dict], operator.add]
    execution: dict      # what the remediation server did (or why it didn't)
    verification: dict   # post-remediation probes: healthy or not
    timeline: dict       # proposed_at, decided_at, executed_at, verified_at (ISO) + derived durations


class Decision(BaseModel):
    approved: bool
    reviewer: str
    note: Optional[str] = None
