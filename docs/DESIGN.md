# Sentinel: system design

An on-call copilot that closes the loop. When an alert fires:
1. an incident opens by itself (one per fault);
2. an agent investigates through tools (deploys, logs, metrics, runbooks);
3. it proposes a root cause with citations and a remediation;
4. it **waits for a human** before anything changes production;
5. on approval, it executes **exactly that action, once**, through a capability-checked deploy adapter;
6. it verifies recovery, records time to mitigate, and writes the postmortem.

This document is the design review: what it does, how it's built, why, and how we know it works. The full reference (low-level design of every service, data model, APIs, operations, phase history) is the [engineering handbook](handbook/README.md).

## 1. Requirements
- **Correct and safe before clever.** A wrong rollback can turn an incident into an outage. Unsafe actions must be blocked by something other than the model's judgment.
- **Grounded.** Every conclusion cites evidence gathered in that run; no invented sources.
- **Multi-tenant.** One tenant can never see or act on another's data, enforced in the database, not only in code.
- **Durable.** Incidents wait hours for approval; they must survive restarts and deploys.
- **Measurable.** Changes to prompts, models, rules or tools are scored against a benchmark in CI.
- **Bounded cost.** Investigations have hard tool-call and dollar budgets.
- **Bounded authority.** The agent never holds standing power over production; each approval authorizes one operation.
- **Verified outcomes.** "Resolved" means the service is measurably healthy again.
- **Zero trust.** No network position or shared secret grants access: only the gateway can mint service credentials, and every hop verifies.

## 2. Architecture

```
 Alertmanager ─(integration token)─┐
 browser ──▶ web (Next.js BFF) ──▶ gateway (Spring Boot 4 + Spring AI) ──▶ agent (LangGraph)
            httpOnly session        JWT · tenancy · Redis rate limits        investigator ⇄ tools
                                    alerts: dedupe + correlate               triage → RCA → guardrails
                                    /ask: RAG + semantic cache               ⏸ human approval
                                    /mcp: search_runbooks (scoped token)     → execute → verify → postmortem
                                    capabilities · audit · incident record   checkpoints (Postgres)
                                        │  ▲                                     │ MCP (streamable HTTP)
              upload ──▶ Kafka ──▶ ingestion ──▶ Postgres (pgvector + FTS)       ▼
                                   chunk → embed → atomic replace          mcp-observability ──▶ services
                                                                           mcp-remediation ──▶ services (capability, once)
```

| Concern | Choice | Why (ADR) |
|---|---|---|
| Platform vs. agent code | Java gateway, Python agent | Each side uses its strongest ecosystem ([0001](adr/0001-polyglot-gateway-and-agents.md)) |
| Human in the loop | LangGraph `interrupt()`; reviewer from JWT; single-use decisions | ([0002](adr/0002-human-approval-before-remediation.md)) |
| Retrieval | pgvector + Postgres full-text in one row, fused with RRF | One system, one transaction, one RLS model ([0003](adr/0003-hybrid-retrieval-in-postgres.md)) |
| Tools | MCP servers discovered at runtime; downscoped 15-min tool tokens; graph-enforced budgets | ([0004](adr/0004-mcp-tools-scoped-tokens-and-budgets.md)) |
| Evaluation | Offline replay of 112 labeled incidents, deterministic scoring, CI gate | ([0005](adr/0005-offline-deterministic-evals-as-a-ci-gate.md)) |
| Safety & ops | Guardrails, pluggable LLM, Redis limits, semantic cache, OpenTelemetry | ([0006](adr/0006-guardrails-and-production-hardening.md)) |
| Deployment | Helm (kind + EKS overlays), Terraform for AWS | ([0007](adr/0007-deployment-kubernetes-and-aws.md)) |
| Identity | OIDC users; gateway-only ES256 service tokens + JWKS; every hop authenticated; checkpoint RLS | ([0009](adr/0009-zero-trust-identity.md)) |
| Closing the loop | Alert intake with dedupe/correlation; single-use capabilities checked by the executing adapter; verification decides `RESOLVED` | ([0008](adr/0008-closed-loop-capabilities-and-alert-intake.md)) |

## 3. Life of an incident
1. **Open.** Alertmanager posts the alert (or an engineer uses the console). For alerts, the gateway takes the tenant from the integration token and, under a per-service advisory lock, deduplicates or correlates onto an open incident. Otherwise it opens one dated at the alert's start. The gateway takes the tenant from the verified JWT (never the body), applies the tenant's rate limit, and writes an `INVESTIGATING` record *before* calling the agent, so a failure leaves `FAILED`, not nothing. It mints a downscoped tool token (tenant, `knowledge:read`, `aud=sentinel-mcp`, 15 min) and calls the agent with `traceparent` and the token in headers.
2. **Investigate.** The investigator (LLM, or a scripted plan when no LLM is configured) calls MCP tools; independent calls run in parallel. Tool output is sanitized before anything sees it (PII redacted, instruction-like text removed, wrapped as untrusted data). The graph enforces ≤ 8 tool calls and ≤ $0.50, and every step is checkpointed to Postgres.
3. **Conclude.** Triage (cheap model) and RCA (strong model) return schema-constrained JSON; the remediation target is an enum of services actually investigated. Guardrails then validate citations against gathered evidence and apply runbook policy: no rollback without a deploy in the 30-minute suspect window, and no action on an uninvestigated service. Violations become `escalate` with the reason recorded.
4. **Approve.** The graph pauses. Any replica can resume it later. The reviewer is the JWT subject. Decisions are single-use. Approval of an executable action mints a capability for the *proposed* action and target (5 min, unique jti).
5. **Execute.** The agent calls the remediation adapter with the capability (kept in config, never in checkpoints). The adapter checks signature, audience, scope, expiry, action and target, then spends the jti in Redis (refusing if Redis is down) before calling the deploy API.
6. **Verify.** Real-traffic probes, up to 6 times. Healthy → `RESOLVED` with time to mitigate; otherwise `REMEDIATION_FAILED`, which stays open for a human.
7. **Record.** Postmortem with hypotheses, citations, execution, verification, timeline, investigation stats and guardrail interventions; an audit row joinable with the adapter's log by jti. One OpenTelemetry trace per request path.

## 4. Data and tenancy
- **Postgres is the system of record.** Incidents (with timeline and TTM), alert links, the remediation audit, knowledge chunks (vector + tsvector), the `/ask` semantic cache and LangGraph checkpoints (own schema).
- **Row-level security** on every tenant table, with `FORCE` so even the owner is subject to it. Services connect as a non-superuser role (superusers bypass RLS: found and fixed in Phase 2) and bind `app.tenant_id` per transaction; unbound reads return nothing.
- **Migrations:** Flyway, owned by ingestion, run as a Helm hook Job with the only admin credentials.

## 5. How we know it works

| Evidence | Where |
|---|---|
| 208 automated tests (unit, integration on real pgvector/Redis via Testcontainers, concurrency on real Postgres, durability across processes, capability attacks, tracing, guardrails, scorer) | each service, `evals/rca/test_scoring.py` |
| RCA benchmark: 112 replayed incidents incl. red herrings, outages, cascades, novel faults, prompt injection; deterministic scoring; held-out set | [`evals/rca/REPORT.md`](../evals/rca/REPORT.md) |
| Baseline: 93% correct remediation, **0% harmful actions, 0% successful injections, 0% PII leaks**; gate fails CI on regression (verified by re-introducing a bug) | same |
| Load: incidents p95 314 ms at 5/s, search p95 18 ms at 20/s, distributed limiter admits exactly its capacity under a 200-request burst | [`docs/load-test.md`](load-test.md) |
| Real Kubernetes: chart installed on kind, migrations hook, hardened pods, NetworkPolicy enforcement verified, incident → approve → executed → verified `RESOLVED` | ADR-0007, CI `kind-e2e` |
| Closed loop, live: alert → incident → approve → execute → verify, **TTM 21–33 s**; a second fault after a rollback → one incident, correct action | handbook ch14 §4, ch15 Phase 7 |

## 6. Failure modes

| Failure | Behavior |
|---|---|
| LLM API down / errors | Investigator falls back to the scripted plan; triage/RCA to rules. The incident still gets a (guarded) proposal |
| A tool fails | Recorded as an error result; investigation continues; policy may escalate (e.g. no deploy history means no rollback) |
| Agent pod dies mid-incident | State is in Postgres checkpoints; any replica resumes |
| Redis down | Rate limiter falls back to per-instance buckets (counted in metrics) |
| Kafka down | Upload returns 503 (never silently dropped); ingestion retries with backoff, poison messages go to the DLT |
| Model proposes something unsafe | Policy interlock downgrades to `escalate`, records why; visible in UI, trace, metrics, postmortem |
| Agent compromised or injected after approval | Can perform at most the one approved operation, once, within 5 minutes; the adapter refuses anything else; the agent pod doesn't hold the signing secret |
| Remediation adapter or its Redis down | Execution refused (fails closed) → `REMEDIATION_FAILED`; the incident stays open |
| The fix doesn't work | Verification fails → `REMEDIATION_FAILED`; the alert stays linked to the incident |
| Alert storm / duplicate deliveries | One incident per fault (dedupe + per-service correlation); per-tenant alerts rate bucket |

## 7. Trade-offs and next steps
- **Synthetic benchmark.** One author wrote the generator and labels. Next: incidents recorded from chaos-lab and real postmortems; the full local-LLM and Claude columns (`--mode llm`).
- **Regex injection filter** is a first layer only; the output policy is the guarantee. Next: a classifier, plus red-team cases written by someone else.
- **Per-call MCP sessions** (about 3 HTTP requests per tool call): pool a session per incident.
- **Image size** (Java ~1.2 GB): platform-specific natives plus jlink.
- **Identity:** keys in a Kubernetes Secret, not a KMS; no mTLS between pods (tokens authenticate, the transport isn't encrypted).
- **Synchronous approval** spans execution and verification (up to about a minute): next is 202 plus streamed progress.
- **Correlation is per service**; cascades across services still open separate incidents.
