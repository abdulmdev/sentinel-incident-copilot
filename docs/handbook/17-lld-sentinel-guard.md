# 17. LLD: Sentinel Guard (v2)

Sentinel Guard is Sentinel's safe-action core (approval → single-use capability → execute once → audit), extracted into a service any agent is held to. It enforces at the Kubernetes API with an admission webhook, so unmodified agents can't route around it, and serves an HTTP API for agents outside Kubernetes. Decision record: [ADR-0010](../adr/0010-sentinel-guard.md). User guide: [docs/guard](../guard/README.md).

## 1. Shape

![Sentinel Guard: who calls what, and where state lives](images/17-guard-architecture.png)

*Figure 17.1: Sentinel Guard: who calls what, and where state lives*

| Part | Where | Role |
|---|---|---|
| `sentinel-guard` | `guard/cmd/sentinel-guard` | The service: two admission webhooks, the action API, JWKS, metrics. Modes `kubernetes` (in a cluster) and `memory` (one process, `docker run`) |
| `kubectl-sentinel` | `guard/cmd/kubectl-sentinel` | The kubectl plugin: `requests`, `describe`, `approve`, `deny`, `audit verify`, `audit export`, and offline `audit verify --bundle` |
| Core | `guard/internal/guard` | Request lifecycle (`engine.go`), policy (`policy.go`), change classification (`classify.go`), capabilities and signatures (`signer.go`), audit chain (`audit.go`), evidence bundle and oversight summary (`evidence.go`), Prometheus preconditions (`precondition.go`) |
| Storage | `guard/internal/store` | `Memory` (one process) and `Kube` (ActionRequest objects, watch cache, compare-and-swap writes) |
| Transport | `guard/internal/server` | HTTP handlers, both webhooks, metrics, the guardian monitor (`monitor.go`) |
| Kubernetes glue | `guard/internal/kube` | Creates its TLS certificate and signing key on first start, keeps the webhook CA bundles, publishes the JWKS, TokenReview authentication |
| Chart | `deploy/helm/sentinel-guard` | CRD, 3 replicas + PDB, RBAC (the guard, approvers, viewers), both webhook configurations, policy ConfigMap |
| Default policy | `guard/policies/default.yaml` | Embedded in the binary |

One static Go binary (37.5 MB linux/amd64, stripped); the image is 11.5 MB compressed on distroless `static:nonroot`.

## 2. Request lifecycle

![The lifecycle of an action request](images/17-guard-lifecycle.png)

*Figure 17.2: The lifecycle of an action request*

A **request** (`guard.Request`) carries:
- what was asked: agent, action, target, the human-readable change, the fingerprint, intent, risk, autonomy;
- the decision: who, when and a note;
- the outcome: phase, reasons, preconditions, expiry, execution, the exact object admitted and its generation;
- its links in the audit chain.

`Engine.Evaluate(proposal)`, in order:
1. **Hard limits** (`Policy.Violations`): autonomy `deny`, a protected namespace (the guard adds its own), a namespace allow-list, `minReplicas`/`maxReplicas`. A violation means **Blocked**: it can't be approved, and repeats within `pendingTTL` reuse one record.
2. **An earlier request for the same key** (`useExisting`):
   - an API-server retry of an executed write is re-admitted (§4);
   - an `Approved` request is consumed (Kubernetes) or yields a capability (API);
   - a `Pending` one is reported again;
   - a recent human denial stands.
3. **A fresh decision.** `observe` is allowed and recorded. `auto` is allowed only if every precondition is observed by the guard. Otherwise a **Pending** request is created, with the guard's precondition results attached.

Dry runs evaluate without recording anything and are admitted with a warning.

**Request ids** are `ar-<first 8 of the key>-<n>`. Concurrent identical attempts compute the same name, so the store's create-if-absent keeps one. An attempt that loses re-evaluates and joins the existing request (`errDuplicate`). The unit test with 50 parallel attempts ends with exactly 2 requests: the executed one and one new pending.

## 3. Classification and the approval binding
`ClassifyAdmission` (`classify.go`) turns an `AdmissionRequest` into an action:

| Request | Action | Fingerprint |
|---|---|---|
| `UPDATE …/scale` | `scale` | `replicas=<n>`. On the wire, Scale's `replicas: 0` is an absent field (`omitempty`); absent is read as 0 there, but as the default 1 for a workload's pointer field |
| `UPDATE` workload, only `spec.replicas` | `scale` | as above |
| only the `kubectl.kubernetes.io/restartedAt` template annotation | `restart` | `restart`. The timestamp is not bound, because kubectl stamps a new one on every attempt |
| container images | `set-image` | `images=<container=image,…>` |
| any other pod-template change | `rollout` | digest of the normalized new template |
| other spec fields, or several kinds at once | `update` | digest(s) |
| `DELETE` | `delete` | `delete` |
| `CREATE` (opt-in) | `create` | digest of the object without metadata |

Metadata-only changes and status or other subresources are not actions.

The **approval key** is a digest of agent + action + target + fingerprint: an approval admits that and nothing else. The proposal also carries:
- `Exact`: a digest of the whole resulting object, incl. the restart timestamp;
- `Generation`: the target's `metadata.generation` before the change.

Both are used only to recognise API-server retries (§4).

## 4. Exactly once on a real API server
- **Consuming an approval** is a compare-and-swap on the `ActionRequest` (`resourceVersion`): `Approved → Executed`. When replicas race, one wins and the others see `Executed`.
- **API-server retries.** When a write conflicts (a controller updated status in between), the API server re-reads the object, re-applies the patch and **runs admission again**. The first call already consumed the approval, so the retry was denied and the approval burned (found by the e2e load test, §8).
  - The guard now re-admits a write that is the **identical resulting object** (`Exact`) **at the same generation**, within `ReadmitWindow` (10 s), at most `MaxReadmits` (5) times, as event `readmitted`.
  - Re-applying an identical object can't change anything twice.
  - A change that landed and was then reverted has moved the generation on twice, so it needs a new approval (found by the same load test).
  - Scale objects carry no generation; the guard reads the workload's (`get` on deployments, statefulsets and replicasets).

## 5. The audit chain
Every event (`requested`, `blocked`, `approved`, `denied`, `capability-issued`, `executed`, `readmitted`, `expired`) is a link:
- `Hash = sha256(seq | prev | type | at | actor | detail | requestId | digest | kid)`.
- `Digest` covers the request's identity fields, so editing what was requested breaks every event of that request.
- `Sig` is an ES256 signature over the hash.

The head (`seq`, `hash`) lives in the ConfigMap `sentinel-guard-audit-head`; the public key in `sentinel-guard-jwks`. Since Phase 14 every new head is also signed (`at`, `kid`, `sig`): a checkpoint (§11).

**Writes:**
1. `transition` first checks the change against the current (cached) state. A refused attempt, such as a replayed capability, writes nothing.
2. It then claims the next place (compare-and-swap on the head).
3. It writes the state change and the event together.

If step 3 loses a race, the claimed place becomes a **signed void** (`Head.Voids`), so the chain stays complete. A void signed by any other key fails verification, so voids can't hide a deletion (unit test).

**Verification** (`VerifyAudit`, run by `kubectl sentinel audit verify` with only read access) checks:
- the sequence is contiguous up to the head;
- every link and signature;
- the digests against the requests;
- the head hash.

It reports: an edited request; a deleted request (a gap); a forged event or void; a broken link; since Phase 14, a forged head signature (an unsigned head from an older guard is only a warning).

## 6. Kubernetes integration
- **Workloads webhook** (validating, `failurePolicy: Fail`, `sideEffects: NoneOnDryRun`, 5 s timeout).
  - Its `matchConditions` select agent groups and users (CEL with `has()` guards, so a missing field can't error and fail closed for everyone).
  - The handler checks `IsAgent` again. Any error for an agent denies (fail closed).
- **Approvals webhook** (mutating, on `ActionRequest` create/update/delete):
  - The guard's own ServiceAccount passes.
  - Creates and deletes by anyone else are refused, even cluster-admin.
  - Spec edits other than the decision are refused.
  - A decision is refused for agent identities, the requester, decided or expired requests. Otherwise it is admitted with a JSON patch that sets `decidedBy` and `decidedAt` from the authenticated identity.
- **Bootstrap.** On first start the guard creates `sentinel-guard-tls` (a CA plus a serving cert for the Service names, 10-year validity) and `sentinel-guard-signing` (a P-256 key). Replicas racing on create read the winner's copy. The guard writes the CA into both webhook configurations, re-checking every minute, so a re-applied manifest heals itself.
- **Reads** come from a watch-fed cache (informer on ActionRequests, indexed by approval key; one on the head ConfigMap), plus this replica's own just-written versions until the watch delivers them. **Writes** are compare-and-swap; a stale read costs a conflict and a fresh read, never correctness. Client throttling is raised (QPS 200, burst 400) because the webhook sits on the request path.
- **Housekeeping** (applying decisions, expiries, the CA bundle) runs every second on the replica holding the `sentinel-guard-housekeeping` Lease (15 s lease, 10 s renew). The admission path also applies a just-made decision inline, so an agent's retry right after an approval doesn't wait for the tick.
- **Agents on the HTTP API** authenticate with their ServiceAccount token (TokenReview, 30 s cache).

![An approval end to end, including an API-server retry](images/17-guard-seq-approval.png)

*Figure 17.3: An approval end to end, including an API-server retry*

## 7. Capabilities (HTTP API)
For agents outside Kubernetes, an approved request yields one ES256 JWT:
- `aud` `sentinel-action`;
- `request_id`, `action`, `target`, `namespace` (the target's scope: a namespace or, for Sentinel, the tenant), `change` (the approved parameters, for the executor to check), `fingerprint`, `approved_by`, `jti`;
- it expires with the approval.

**Delegated decisions (Phase 11).** A service that authenticated a person itself may record that person's decision over HTTP, if its identity is listed under the policy's `delegates` (the admin token is the only other way, and only in single-process mode).
- The request stores `decidedBy` = the person and `decidedVia` = the delegate; the audit event reads "note [via delegate]".
- A delegate can't decide on behalf of the requester, and the requester can't be the delegate.
- Sentinel's own gateway is the first delegate: the copilot's console approvals go through it ([ADR-0011](../adr/0011-copilot-approvals-through-the-guard.md)).

**In-cluster API listener.** With `SENTINEL_GUARD_API_ADDR` (chart `api.plainHTTP`), the guard also serves the action API, without the admission webhooks, over plain HTTP. Clients in other namespaces can't trust the webhook's private CA; a NetworkPolicy limits who reaches the port.

It is issued once (`capability-issued` records the `jti`). The executor calls `POST /v1/capabilities/spend`, which verifies it (algorithm pinned, issuer, audience, expiry) and moves the request `Approved → Executed` only if the `jti` matches: single use, enforced by the store's compare-and-swap. The API's fingerprint is a digest of the proposed `change` map, so an approval for €42 doesn't cover €4,200 (unit test).

## 8. Measured
All numbers from `scripts/guard-e2e.sh` with `LOAD=1` on a fresh kind cluster (1 node, Docker Desktop, Apple Silicon), 3 guard replicas, re-measured 2026-10-24 after Phase 14 (which adds a signed checkpoint to every event). Raw data: [`evals/guard/latency.json`](../../evals/guard/latency.json). Latency is measured by the client through the API server (TLS, the webhook call, the guard's work, the write), with `test/loadgen`.

| Scenario | n | p50 | p95 | p99 | max |
|---|---|---|---|---|---|
| Human scales a deployment (the guard is never called) | 400 | 3.2 ms | 5.6 ms | 8.8 ms | 35.7 ms |
| Agent repeats a change that's pending (denied after a cache lookup) | 400 | 2.4 ms | 4.7 ms | 6.0 ms | 14.8 ms |
| Agent's change after a human approved it (admitted once) | 100 | 24.7 ms | 31.9 ms | **40.8 ms** | 44.0 ms |
| Worst case: approval and retry in the same instant (the decision is applied on the request path) | 100 | 28.5 ms | 37.0 ms | 45.6 ms | 56.6 ms |

![Sentinel Guard: p99 latency through the API server](images/17-chart-guard-latency.png)

*Figure 17.4: Sentinel Guard: p99 latency through the API server*

- **What a pending check costs:** no more than no guard at all (2.4 vs 3.2 ms at p50), because it is served from the watch cache.
- **The approved path** makes two writes: the chain head, and the request's state together with its event. p99 is 40.8 ms, under the 50 ms target.
- **Run-to-run variation.** The Phase 10 run measured 42.9 ms on the approved path and 4.9 ms for the human baseline. The human path never calls the guard, so its rise to 8.8 ms here is the host: a laptop, with other work running during this run. The extra checkpoint signature costs about 26 µs (`BenchmarkSignHash`), which these numbers can't resolve. Read the table as "tens of milliseconds for an approved change, single digits otherwise".
- **Safety under load:** every load round also asserts that an unapproved change is denied, that a different change doesn't ride on an approval, and that the same change after a revert needs a new approval. The chain verified after 615 events across 211 requests, **with no voids** (no lost races).
- **Guard down:** humans' changes still go through and agents' changes are refused. After scaling back up from zero, the guard answered again within **2 s** (1 s in the Phase 10 run).
- **The end-to-end suite:** 31 checks, all passing (chapter 14). The 6 added in Phase 14 cover policy versions, the signed head, offline verification of the export, tamper detection, every replica's self-verification, and a later export extending an archived one.

## 9. Configuration
Environment variables and chart values: [chapter 13 §8](13-deployment-and-operations.md#8-sentinel-guard). Metrics: [chapter 12 §2](12-observability.md#2-metrics-catalog-prometheus). Endpoints and the CRD: [chapter 09 §8](09-api-and-contracts.md#8-sentinel-guard).

## 10. SDKs and the MCP proxy (Phase 12)

![Every kind of agent, one guard: webhook, SDKs, the LangChain wrapper and the MCP proxy](images/17-guard-adoption.png)

*Figure 17.5: Every kind of agent, one guard: webhook, SDKs, the LangChain wrapper and the MCP proxy*

Design record: [ADR-0012](../adr/0012-sdks-and-the-mcp-proxy.md).

| Piece | Where | What it does |
|---|---|---|
| Python SDK | `sdk/python/sentinel_guard/client.py` | `Guard` / `AsyncGuard`: `propose`, `status`, `require(…, wait)` (polls `GET /v1/actions/{id}` until allowed, blocked or out of time), `spend`, the `guarded` decorator (approval bound to all arguments by default; `target` and `change` as values, `"{arg}"` templates or callables) |
| LangChain wrapper | `sdk/python/sentinel_guard/langchain.py` | `guard_tool(tool, guard, …)`: same name, description (plus a note) and arguments; while not allowed, it returns an explanation **to the model** instead of acting |
| MCP proxy | `sdk/python/sentinel_guard/mcp_proxy.py`, CLI `sentinel-guard-mcp-proxy` | stdio MCP server in front of any MCP server (stdio or streamable HTTP). Per tool `pass`, `guard` or `deny`; upstream results returned unchanged; refusals are error results |
| TypeScript SDK | `sdk/typescript/src/index.ts` | `Guard`: the same `propose`, `status`, `require`, `spend`, `guarded`; zero dependencies |

**Errors fail closed in both SDKs:**
- `ApprovalPending` (with the request id to show a human);
- `ActionBlocked` (policy, or a human denied it);
- `CapabilityRejected` (already used, expired, forged);
- `GuardUnavailable` (unreachable, or any unexpected status such as a 401: never read as permission).

**The change encoding is canonical and shared.** Strings stay as they are; anything else becomes compact JSON with sorted keys (`canonical_change` / `canonicalChange`). The approval binds to the fingerprint of these values, so both SDKs must agree byte for byte; a golden value is asserted in both test suites.

**Where the capability is redeemed decides the protection** (ADR-0012 §4):
- **The decorator and the wrapper** redeem in the agent's own process. That stops the model's mistakes and manipulation, not a compromised process.
- **A separate executor, the MCP proxy, or the webhook** also stop a compromised agent.

**Measured:** both SDKs and the proxy run against the real guard binary in CI (`sdks` job).
- Python: 19 tests, including the proxy launched by a real MCP client over stdio, launching a real upstream server.
- TypeScript: 6 tests.
- The refund example (`examples/guarded-refunds`) ran as documented: pending → approved with the printed `curl` → refunded once → the same refund again opened a new request.


## 11. Governance: evidence, policy versions, guardian monitoring (Phase 14)

![Evidence and guardian monitoring: what is signed, where it lives, and who checks it](images/17-guard-evidence.png)

*Figure 17.6: Evidence and guardian monitoring: what is signed, where it lives, and who checks it*

Design record: [ADR-0014](../adr/0014-governance-evidence-and-guardian-monitoring.md). User-facing: [docs/compliance](../compliance/README.md).

**Policy versions.**
- `Policy.Canonical()` marshals the *effective* policy (defaults applied; the guard's own namespace and memory-mode delegates added). Its SHA-256 is the version.
- `Engine.SetPolicy` computes it once. `Engine.RecordPolicy` signs `sha256("policy|version|loadedAt")` and stores the record (Memory, or one key per version in the ConfigMap `sentinel-guard-policies`); a version already stored keeps its first record.
- `create()` records the version before the first request that names it, so no request can name an unkept policy.
- `Request.PolicyVersion` joins the request digest (only when set, so pre-Phase-14 requests still verify).

**Signed checkpoints.** `chain()` signs the new head (`sha256("head|seq|hash|at")`) in the same compare-and-swap that claims the event's place. That is one extra ES256 signature per event: about 26 µs (`BenchmarkSignHash`, M1 Pro). Voids keep the head's signature; they are signed themselves.

**The evidence bundle** (`evidence.go`).
- `BuildEvidence` orders requests by their first event and policies by load time, and attaches `Summarize(requests)`.
- `VerifyEvidence(bundle, keys)` runs `VerifyAudit`, then checks:
  - each policy (hash of its text, signature);
  - that every request's version is kept;
  - each request's phase, decision and decider against its signed events (`consistency`);
  - the summary against a recomputed one.
- `keys == nil` means "the bundle's own keys", reported by ID.
- `Engine.Verify` now goes through the same path, so the HTTP verify, the monitor and `kubectl sentinel audit verify` check exactly what an auditor checks.

**The oversight summary** derives everything from signed events:
- outcome counts overall and per agent: `proposed` / `blocked` / `auto_approved` / `approved` / `denied` / `executed` / `expired` (`EventOutcome`, shared with the metric);
- who decided (a person, or "person via delegate");
- expiries nobody decided;
- median and longest time from request to human decision.

**Anchoring.** `Continues(older, newer)` requires the head not to go back, and every event and void of the older bundle to be present and unchanged in the newer one (`audit verify --previous`). That catches the one thing a signed head can't: a restored older signed head with the newest events removed.

**The guardian monitor** (`server/monitor.go`, every replica, from its watch cache):
- every 15 s: open requests by phase, the oldest undecided request's age, the head's sequence;
- every `SENTINEL_GUARD_VERIFY_INTERVAL` (5 min): a full `Verify`, giving `sentinel_guard_audit_chain_ok` and a timestamp; a failure is also logged with the problems;
- `sentinel_guard_agent_outcomes_total` is counted in the event hook;
- the alert rules are in chapter 12.

**Measured** (`BenchmarkVerifyEvidence`, Apple M1 Pro): verifying 1,000 approved-and-executed requests (3,000 events) takes **about 0.18 s**, almost all of it ECDSA verification. The bundle is **about 2.1 KB per request** (compact JSON).

**Bug found and fixed.** The CRD schema lacked `spec.decidedVia`, so the API server pruned it. Delegated decisions in Kubernetes mode (the copilot's path since Phase 11) kept the delegate only in the signed event's detail. The schema now has `decidedVia` and `policyVersion`; the e2e checks that `policyVersion` survives.
