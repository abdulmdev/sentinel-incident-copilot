# ADR-0002: Human approval before any remediation

**Status:** accepted, 2026-10-05

## Context
LLMs can be wrong with high confidence, and logs are attacker-controllable input that can carry prompt injection. A rollback or restart in the wrong place can turn an incident into an outage.

## Decision
- The LangGraph workflow always pauses at an `approval` node (`interrupt()`) before remediation.
- The reviewer identity is the JWT `sub` from the gateway, never a field in the request body.
- Decisions are single-use: deciding an incident that isn't `AWAITING_APPROVAL` returns 409.
- Remediation actions come from a closed enum (`rollback`, `restart`, `scale_out`, `increase_pool`, `failover`, `escalate`), so the model cannot make up an action.

## Consequences
- + Safe by default, with an audit trail of who approved what.
- − Adds human latency to time-to-mitigation. Later, low-risk actions such as `scale_out` could be auto-approved per tenant policy, gated on measured eval precision.
