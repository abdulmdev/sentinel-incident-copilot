# ADR-0011: The copilot's approvals and capabilities go through Sentinel Guard

**Status:** accepted, 2026-10-24. Supersedes the capability-minting parts of [ADR-0008](0008-closed-loop-capabilities-and-alert-intake.md) and [ADR-0009](0009-zero-trust-identity.md): the gateway is no longer the issuer of capabilities, and the adapter no longer keeps its own replay store. Builds on [ADR-0010](0010-sentinel-guard.md).

## Context
After Phase 10 the repository had two approval paths:
- **Sentinel Guard:** any agent proposes; a human approves; the guard issues a single-use capability and keeps a signed audit chain.
- **The incident copilot's own (v1):** the gateway minted a capability at approval, and the remediation adapter spent its `jti` in Redis.

Two implementations of the safety core is one too many. It's also the weaker one that guarded the flagship product: no independent audit chain, approvals not tied to the exact parameters, and a second replay store to operate.

## Decisions

### 1. The agent proposes; the guard owns the approval and the capability
- **The agent registers its proposal** in a new graph node, `request_approval`, between `rca` and the `approval` interrupt: `POST /v1/actions` with
  - action;
  - target `{namespace: tenant, name: service}`;
  - change `{incident, size | replicas}`;
  - intent.

  It's a node of its own because LangGraph re-runs an interrupted node from the top on resume. A proposal sent again after the approval would collect (and discard) the capability.
- **After approval**, `execute` collects the capability from the guard (`GET /v1/actions/{id}`, issued once) and passes it straight to the adapter. It is never in state, so never checkpointed.
- **The gateway mints no capabilities.** `RemediationTokens` is deleted. The gateway's other service tokens (tool, agent-call, integration) are unchanged.

### 2. The gateway records reviewers' decisions as a *delegate*
- The console user is authenticated by the gateway (OIDC), not by the guard.
- The gateway calls `POST /v1/actions/{id}/approve|deny` with **its own** identity: a projected ServiceAccount token in Kubernetes, a static token locally. The `approver` is the JWT subject.
- The guard accepts that only from identities listed under `delegates`. It records the decision as the person's, with `decidedVia` = the gateway, and the audit event reads `alice@acme … [via …gateway]`.
- **Ordering:** the decision is recorded in the guard **before** the agent is resumed. If the guard refuses (expired, already decided, unreachable), nothing resumes and the console gets the reason.
- **Rejections are recorded too,** so a rejected request can never be approved later.
- **Separation of duties holds:** the requester is the agent's identity and the delegate is the gateway's. The guard refuses a decision by the requester, or one made through it.

### 3. The adapter verifies with the guard's key and redeems at the guard
- **Verification:** ES256 against the guard's JWKS; `iss sentinel-guard`, `aud sentinel-action`, expiry, `action`, `target`.
- **Every parameter must equal the approved one** (`change.size`, `change.replicas`). "Increase the pool" approved at 40 can't run at 60.
- **Then it redeems** the capability (`POST /v1/capabilities/spend`), which the guard consumes atomically. If the guard can't be reached, nothing executes (fail closed).
- The adapter's Redis replay store and its `redis` dependency are removed. Single use is enforced in one place, for all executors.

### 4. Deployment
- **Helm:** `sentinel-guard` is a dependency of the `sentinel` chart.
  - The agent and gateway get **their own ServiceAccounts** (before, both ran as the namespace default, which the guard could not tell apart). They present short-lived projected tokens: 1 h, rotated by the kubelet, re-read on every call.
  - In-cluster clients use the guard's plain-HTTP API port, which serves no webhooks (they can't trust the guard's private CA across namespaces). A NetworkPolicy admits only the agent, the gateway and the adapter to it, and the API server to the webhook port.
- **Locally** (`scripts/dev-local.sh`, compose): the guard runs as one process on `:8400` with dev-only tokens and `deploy/guard/copilot-policy.yaml`.
- **Policy:** pending requests wait up to 72 h (incidents can wait for a human); approvals are usable for 10 minutes.

### 5. Kept as they were
- **The agent's interlocks** (citations, deploy window, runbook preconditions) still decide *what a human is shown*. They're evaluated on the agent's evidence and gated by the RCA benchmark.
- **The guard decides *what may run*.** The two layers stay separate: moving the interlocks into the guard would change measured benchmark behaviour without a gain.

## Consequences
- **Positive:**
  - One safety core for the copilot and for any other agent.
  - Every copilot approval is in a signed, independently verifiable audit chain (`remediation_audit.guard_request` links the two).
  - Approvals are bound to the exact parameters.
  - One fewer stateful dependency for the adapter.
- **Negative:**
  - **The guard is on the remediation path.** If it's down, nothing executes (by design), and new proposals record the error instead of a request.
  - **The guard trusts the gateway's word for who the reviewer is** (as v1 trusted the gateway to mint). Verifying the user's own OIDC token at the guard is on the roadmap.
  - **Identities are configuration.** The chart's agent and delegate entries assume the release and namespace `sentinel`.
  - **Single-process dev guard:** pending approvals are lost when the dev guard restarts.
