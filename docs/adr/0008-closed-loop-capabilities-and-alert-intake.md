# ADR-0008: Close the loop: alert intake, capability-gated execution, verified recovery

**Status:** accepted, 2026-10-06 (supersedes the "humans execute" consequence of [ADR-0002](0002-human-approval-before-remediation.md); human approval itself is unchanged)

## Context
Through Phase 6 Sentinel stopped at a recorded decision. Three gaps meant it couldn't claim to reduce time to mitigate (TTM):
- **Opening:** a human had to notice the alert and open the incident by hand.
- **Execution:** after approval, a human still had to run the rollback or restart somewhere else.
- **Outcome:** nobody checked that the fix worked, so "RESOLVED" meant "approved", not "healthy".

Letting an LLM-driven agent run production operations is the riskiest step in the project. The design has to hold even if the agent is wrong, prompt-injected or compromised.

## Decisions

### 1. Alerts open incidents (gateway, `POST /api/v1/alerts/alertmanager`)
- Alertmanager's standard webhook. It is authenticated with a per-tenant **integration token**: audience `sentinel-alerts`, scope `alerts:write`. The security rule requires **both** claims. That token works on this endpoint only, and user and agent tokens are refused there.
- **Tenant** comes from the token; labels on the alert can't choose it.
- **`opened_at` is the alert's `startsAt`**, so deploy timing and TTM are measured from when the problem began, not when we heard about it.
- **Deduplication and correlation** in one transaction under a per-(tenant, service) advisory lock:
  - same fingerprint with an active incident → deduplicated;
  - another alert on the same service with an active incident → correlated onto it, with no second investigation;
  - otherwise a new incident.
  The partial unique index `(tenant_id, fingerprint) WHERE active` is the backstop.
- **Response before investigation:** the webhook answers 202 at once and investigates on a context-propagating virtual-thread executor, so Alertmanager's timeout never races the LLM.
- **Resolved notifications** stamp `alert_resolved_at` once the incident's *last* linked alert clears. They don't close the incident: verification or a human does.

### 2. Approval mints a capability; only a capability executes
- On approval of an executable action, the gateway mints a JWT capability:
  - audience `sentinel-remediation`, scope `remediation:execute`;
  - bound to **tenant, incident, action, target and approver**;
  - **5-minute** TTL, unique `jti`.
- It travels gateway → agent in a header and agent → remediation adapter as the bearer. The agent keeps it **in run config only, never in checkpointed state**.
- The **remediation MCP server** (deploy-tool adapter) checks, before any side effect:
  - signature, audience, scope and expiry;
  - `action` equals the tool being called and `target` equals the service argument;
  - then **records the jti in Redis (`SET NX`) for single use**. If Redis is unreachable it **refuses** (fails closed).
- `dry_run` validates a capability without consuming it.
- Rejection and approved `escalate` mint nothing.
- **Containment:** a compromised agent holding a capability can perform exactly one approved operation on one service, once, within 5 minutes. The gateway rejects capabilities on every API (they're a service audience), and NetworkPolicy lets only the agent reach the adapter.

### 3. Verify, then call it resolved
- After execution the agent runs `probe_service` (real synthetic traffic, concurrent, 3-second per-request cap).
- **Healthy** means success ≥ 95% and p95 < 1 s, checked up to `SENTINEL_VERIFY_ATTEMPTS` (6) times, `SENTINEL_VERIFY_INTERVAL_S` (5 s) apart.
- Healthy → `RESOLVED`, with `time_to_mitigate_s` = verified_at − opened_at.
- Not healthy → `REMEDIATION_FAILED`: the incident stays open, its alert stays linked and a human takes over.
- New statuses:
  - `REMEDIATING` (between decision and outcome);
  - `ESCALATED` (approved escalation: nothing to execute);
  - `REMEDIATION_FAILED`.

### 4. Audit in two places
- The gateway writes `remediation_audit`: approver, capability jti, executed, verified and outcome.
- The adapter logs one JSON audit line per execution with the same jti.
- The two can be joined to show that every operation had an approval and every approval at most one operation.

## Alternatives considered
- **Agent holds standing deploy credentials, gated by a flag in state.** Rejected: a prompt-injected or buggy agent would hold a key that can do anything, any number of times.
- **Gateway executes directly (no agent in the path).** Simpler, but loses the agent's verification loop and trace continuity, and the agent would still need its own path for verification. The capability gives the same containment with the agent in the loop.
- **OPA/policy service for authorization.** Heavier than needed. Binding the approval to claims in a token is enough for one action per approval.
- **Auto-approve low-risk actions.** Deferred. It needs measured precision per action from the benchmark and a per-tenant policy (see chapter 16).

## Consequences
- + Measured outcome. In the local live run, alert-to-verified TTM was 21–33 s, including the human decision; the proposal was ready 6 s after the alert started.
- + The blast radius of any agent failure is one approved operation.
- + Duplicate and racing alerts can't open parallel investigations of one fault (concurrency ITs on real Postgres).
- − The approval request now spans execution and verification (up to about a minute). It's synchronous for simplicity; a production system would return 202 and stream progress.
- − Correlation is per service. A fault that alerts on two services (checkout errors caused by inventory) still opens two incidents. Cross-service correlation needs the dependency graph (roadmap).
- − Redis becomes a hard dependency for execution (by design: fail closed), although not for investigation.
