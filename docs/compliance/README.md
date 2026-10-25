# Governance and compliance with Sentinel Guard

Sentinel Guard decides what AI agents may change and keeps a record of it. Two things follow from that:
- Before an action: a person (or a policy you wrote) approves that exact change, once.
- After it: you can prove what happened, to anyone, from one file.

This folder covers:
- how the guard's evidence works;
- how its features map to the **EU AI Act** ([eu-ai-act.md](eu-ai-act.md)) and the **NIST AI Risk Management Framework** ([nist-ai-rmf.md](nist-ai-rmf.md));
- what still falls to you.

> **Not legal advice.** Sentinel Guard is a tool. The obligations in these frameworks fall on the providers and deployers of AI systems. Whether a system is "high-risk" under the AI Act depends on its intended purpose. For example, an agent used as a safety component in operating critical digital infrastructure falls under Annex III point 2. Use these pages as a map of where the guard produces evidence, and have counsel decide what applies to you.

## What the guard records, and how you show it

| You need to show | What the guard does | How to show it |
|---|---|---|
| Every agent action is logged automatically | Each proposal, policy block, human decision, capability, execution and expiry is an event in a signed, hash-chained log. The guard writes it, not the agent. In Kubernetes, an agent can't reach the API without passing the admission webhook. | `kubectl sentinel audit export > evidence.json` |
| The log hasn't been altered | Every event, void, policy version and the chain head (a checkpoint) is signed with the guard's ES256 key. Verification recomputes everything else. | `kubectl sentinel audit verify --bundle evidence.json --jwks keys.json`, offline |
| People oversee actions before they happen | Agents wait for approval of the exact change. Agents and the requester can't approve. The approver is the identity the API server (or a trusted delegate) authenticated. | The bundle's oversight summary: who decided what, how fast, and what expired with nobody deciding |
| People can override or stop the system | Deny a request; set an action's autonomy to `deny`. **Stop every agent at once:** scale the guard to zero. Agents' changes are then refused (fail closed) while humans, CI and controllers carry on. | The e2e suite proves the stop works on a real API server ([guard-e2e.sh](../../scripts/guard-e2e.sh), "Chaos: the guard is down") |
| Decisions trace to the rules in force | Each request names the policy version it was evaluated under: the SHA-256 of the effective policy. The guard keeps each version, signed, with when it was first loaded. | The bundle's `policies`; each request's `policyVersion` |
| Operation is monitored | Per-agent outcome metrics, an approval-backlog gauge, and continuous self-verification of the chain. Alert rules cover integrity, oversight lapses and drift in each agent's behaviour. | `sentinel_guard_*` metrics; the chart's `prometheusRule` ([alerts](../../deploy/helm/sentinel-guard/alerts/guard-alerts.yaml)) |
| Logs are kept long enough | Records live as `ActionRequest` objects that nobody can delete, not even cluster-admin. Exports archive them anywhere. | Archive exports on a schedule (below) |

## The evidence bundle

One JSON file (`sentinel-guard-evidence/v1`) holds:
- every request: what the agent asked, the guard's own precondition checks, the decision and outcome, and its signed audit events;
- the signed chain head;
- every policy version, signed;
- the guard's public key(s);
- an oversight summary.

There are two ways to get it, with the same content:
- `kubectl sentinel audit export > evidence.json`: your own RBAC; the `sentinel-guard-viewer` role is enough;
- `GET /v1/audit/export` on the guard: the admin token in single-process mode, or an identity listed under the policy's `auditors`.

The bundle has **no signature of its own, and needs none**. Every record in it is signed by the guard, and the verifier recomputes the summary instead of trusting it. So it doesn't matter who produced the file or how it travelled.

**What `kubectl sentinel audit verify --bundle` checks.** It needs no cluster access.

| Check | What it catches |
|---|---|
| Sequence contiguous up to the head; links; every event's signature | A deleted record (a gap), a forged or reordered event |
| Each request's digest against its events | An edited request: what was asked, its target, change or policy version |
| Each request's phase, decision and decider against its signed events | An edited outcome, such as "approved by mallory" |
| The head's signature | A head moved to hide the newest events, without the guard's key |
| Each policy version's hash and signature | An edited or substituted policy |
| Every request names a kept policy version | A request whose rules aren't in the evidence |
| The summary against the records | A doctored summary |

**Which key to trust.** By default the verifier uses the key in the bundle and prints its key ID. A forger could re-sign a whole history with their own key, and it would verify against itself. So compare the key ID with one you recorded independently, or pass `--jwks`. The guard publishes its key in the `sentinel-guard-jwks` ConfigMap and at `/.well-known/jwks.json`.

**Anchoring.** A signed head stops anyone without the key from cutting the newest events. It can't stop someone who saved an older signed head from restoring it after deleting what followed. Archived exports close that gap: each new export must extend the last one.

```bash
kubectl sentinel audit verify --bundle evidence-2026-11.json --jwks keys.json --previous evidence-2026-10.json
```

That fails if any event recorded in October's bundle is missing or changed in November's, or if the chain went back. The `SentinelGuardAuditHeadWentBack` alert watches the same thing continuously, from Prometheus's own history.

## Archiving (retention)

The AI Act asks providers (Art. 19(1)) and deployers (Art. 26(6)) to keep automatically generated logs for a period appropriate to the system's purpose, of at least six months, unless other law says otherwise. The guard never deletes its records. The bundles are also the unit to archive outside the cluster: in write-once storage (for example S3 Object Lock in compliance mode), under your own retention rules.

```bash
# Monthly, from a machine with the viewer role (or a CronJob whose ServiceAccount is a policy auditor)
f=evidence-$(date -u +%Y-%m).json
kubectl sentinel audit export > "$f"
kubectl sentinel audit verify --bundle "$f" --jwks keys.json --previous evidence-2026-10.json   # last month's
aws s3 cp "$f" s3://your-evidence-bucket/sentinel-guard/
```

Size: about **2.1 KB per request** (three events: requested, approved, executed). Verifying 1,000 requests (3,000 events) takes about **0.18 s** on an Apple M1 Pro. Both are measured by `BenchmarkVerifyEvidence` in `guard/internal/guard/evidence_test.go`.

## Guardian monitoring

The guard watches the agents it guards, and itself. Every replica:
- counts each agent's outcomes;
- tracks how long the oldest request has waited for a person;
- re-verifies the whole chain every 5 minutes (`SENTINEL_GUARD_VERIFY_INTERVAL`).

The chart ships alert rules on those series (`prometheusRule.enabled=true`), unit-tested with `promtool`:

| Alert | Fires when | Why it matters |
|---|---|---|
| `SentinelGuardAuditChainBroken` | A replica's self-verification fails | A record was edited, deleted or forged |
| `SentinelGuardAuditNotVerified` | No replica verified the chain for 30 minutes | The integrity check itself stopped |
| `SentinelGuardAuditHeadWentBack` | The chain head's sequence went down | The newest events were cut |
| `SentinelGuardApprovalBacklog` | An agent has waited over 30 minutes for a person | Oversight isn't keeping up; requests will expire undecided |
| `SentinelGuardAgentBlockedBurst` | An agent hit policy limits 5+ times in 10 minutes | A looping agent, or one following injected instructions |
| `SentinelGuardAgentRejectionDrift` | An agent's blocked-or-denied share in the last hour is over 25%, and twice its 7-day rate | Its behaviour changed |
| `SentinelGuardHumanOverrides` | Reviewers denied over half of an agent's requests in 24 hours (at least 4 decisions) | People no longer accept what it proposes |
| `SentinelGuardAgentEscalationDrift` | An agent needs a person for over half its actions, and twice its usual share | Preconditions that let it act on policy are no longer observed |

Thresholds are starting points; tune them to your agents' volume.

## Try it

`scripts/guard-demo.sh` runs the whole story in about a minute:
1. a refund agent asks;
2. a person approves;
3. it runs once, and a replay is refused;
4. the evidence is exported and verified offline;
5. an edited copy is caught.

It needs no Docker. In GitHub Codespaces, open the repository with the "Open in GitHub Codespaces" button in the [README](../../README.md); the walkthrough is ready to run.
