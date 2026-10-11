"""Deterministic guardrails around the model: it proposes, policy disposes.

Inputs (tool output, before any model or checkpoint sees it):
  - redact_pii: emails, card numbers (Luhn-checked), JWTs, API keys -> [REDACTED:<kind>]
  - neutralize_injection: log text is written by services and, through them, by attackers.
    Instruction-like spans are cut out and the event recorded.

Outputs (RCA, before a human sees it):
  - validate_citations: every citation must name evidence gathered in this run. Recognizable
    formats are normalized ("[logs:checkout] ERROR ..." -> "logs:checkout"); anything else is dropped.
  - enforce_policy: remediation interlocks from the runbooks, e.g. no rollback without a deploy
    inside the suspect window. Violations are downgraded to `escalate`, never silently changed.

Every intervention becomes a guardrail event in state, so evals can report raw vs. guarded behavior.
"""
from __future__ import annotations

import re
from datetime import datetime, timedelta

from .heuristics import DEPLOY_SUSPECT_WINDOW, _DEPLOY_TS
from .models import Evidence, Incident, RcaResult, Remediation

# --- PII ---------------------------------------------------------------------------------------

_EMAIL = re.compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")
_JWT = re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b")
_API_KEY = re.compile(r"\b(?:sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{30,})\b")
# Card numbers: 13-19 digits (optionally grouped), card-network prefix 3-6, never part of a larger
# number. Lookarounds matter: the fraction of a float like 16.532024897924202 passes Luhn by chance.
_CARD = re.compile(r"(?<![\w.])[3-6]\d(?:[ -]?\d){11,17}(?!\w|\.\d)")


def _luhn(digits: str) -> bool:
    total, alt = 0, False
    for d in reversed(digits):
        n = int(d)
        if alt:
            n = n * 2 - 9 if n > 4 else n * 2
        total, alt = total + n, not alt
    return total % 10 == 0


def redact_pii(text: str) -> tuple[str, dict[str, int]]:
    counts: dict[str, int] = {}

    def sub(pattern, kind, s, check=None):
        def repl(m):
            if check and not check(m.group(0)):
                return m.group(0)
            counts[kind] = counts.get(kind, 0) + 1
            return f"[REDACTED:{kind}]"
        return pattern.sub(repl, s)

    text = sub(_JWT, "jwt", text)
    text = sub(_API_KEY, "api_key", text)
    text = sub(_EMAIL, "email", text)
    text = sub(_CARD, "card", text, check=lambda s: _luhn(re.sub(r"\D", "", s)))
    return text, counts


# --- Prompt injection --------------------------------------------------------------------------

_INJECTION = re.compile(
    r"(ignore (?:all |any )?(?:previous|prior|above) (?:instructions|prompts?)"
    r"|disregard (?:the |all )?(?:previous|prior|above|system)"
    r"|you are now\b|new instructions?:|system prompt|developer mode|maintenance mode"
    r"|<\|im_start\|>|<\|system\|>|\[/?INST\]|###\s*(?:system|instruction)"
    r"|(?:assistant|agent|ai)\s*[:,]\s*(?:propose|recommend|run|execute|call|roll ?back|restart)"
    r"|(?:propose|recommend|execute|perform) (?:an? )?(?:immediate )?(?:rollback|restart|failover)\b)",
    re.IGNORECASE)


def neutralize_injection(text: str) -> tuple[str, int]:
    """Cuts instruction-like spans (to end of their sentence) out of untrusted text."""
    hits = 0

    def repl(m):
        nonlocal hits
        hits += 1
        return "[removed: instruction-like text in untrusted data]"
    pattern = re.compile(rf"(?:{_INJECTION.pattern})[^.\n\"}}]*[.!]?", re.IGNORECASE)
    return pattern.sub(repl, text), hits


def sanitize_tool_output(text: str) -> tuple[str, list[dict]]:
    events = []
    text, pii = redact_pii(text)
    if pii:
        events.append({"type": "pii_redacted", "counts": pii})
    text, injections = neutralize_injection(text)
    if injections:
        events.append({"type": "injection_neutralized", "count": injections})
    return text, events


# --- Output validation -------------------------------------------------------------------------

_BRACKETED = re.compile(r"^\s*\[([^\]]+)\]")


def validate_citations(rca: RcaResult, evidence: list[Evidence]) -> tuple[RcaResult, list[dict]]:
    known = {e.source for e in evidence}
    events = []
    for cause in rca.root_causes:
        kept = []
        for c in cause.citations:
            candidate = c.strip()
            m = _BRACKETED.match(candidate)
            if m:
                candidate = m.group(1).strip()
            if candidate not in known:  # e.g. "logs:checkout (ERROR x40)" -> longest known prefix
                candidate = max((k for k in known if c.strip().startswith(k)), key=len, default=candidate)
            if candidate in known:
                if candidate != c:
                    events.append({"type": "citation_normalized", "from": c[:120], "to": candidate})
                if candidate not in kept:
                    kept.append(candidate)
            else:
                events.append({"type": "citation_dropped", "citation": c[:120]})
        cause.citations = kept
    return rca, events


def _recent_deploy(service: str, evidence: list[Evidence], opened_at: datetime) -> bool:
    for e in evidence:
        # Only a running deploy counts: not "no recent deployments", a rollback, or a rolled-back build.
        if e.source != f"deploys:{service}" or not e.detail.startswith("deployment completed"):
            continue
        m = _DEPLOY_TS.search(e.detail)
        if not m:
            return True  # timing unknown: don't block
        try:
            if timedelta(0) <= opened_at - datetime.fromisoformat(m.group(1)) <= DEPLOY_SUSPECT_WINDOW:
                return True
        except ValueError:
            return True
    return False


def enforce_policy(incident: Incident, rca: RcaResult, evidence: list[Evidence]) -> tuple[RcaResult, list[dict]]:
    rem = rca.remediation
    observed_services = {e.source.split(":", 1)[1] for e in evidence
                         if ":" in e.source and not e.source.startswith("runbook:")}
    events: list[dict] = []
    if rem.target not in observed_services:
        mentioned = [s for s in observed_services if re.search(rf"\b{re.escape(s)}\b", rem.target)]
        if len(mentioned) == 1:  # "checkout database connection pool" -> "checkout"
            events.append({"type": "target_normalized", "from": rem.target, "to": mentioned[0]})
            rem.target = mentioned[0]
    reason = None
    if rem.action != "escalate" and rem.target not in observed_services:
        reason = f"target {rem.target!r} was never investigated"
    elif rem.action == "rollback" and not _recent_deploy(rem.target, evidence, incident.opened_at):
        reason = (f"no deploy of {rem.target!r} within {int(DEPLOY_SUSPECT_WINDOW.total_seconds() // 60)} "
                  "minutes before the alert (runbook: roll back only deploy-correlated incidents)")
    if reason is None:
        return rca, events
    events.append({"type": "action_overridden", "from": rem.action, "target": rem.target, "reason": reason})
    rca.remediation = Remediation(action="escalate", target=rem.target, risk="low",
                                  reason=f"Policy blocked {rem.action}: {reason}. Original rationale: {rem.reason}")
    return rca, events
