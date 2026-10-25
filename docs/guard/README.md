# Sentinel Guard

**Envelope protection for AI agents.** No agent changes your cluster unless policy, or a human, approved *that exact change*, and then it is admitted **once**. Humans, CI and Kubernetes controllers are never slowed down or stopped.

Guard is the safety core of [Sentinel](../../README.md) (approval → single-use capability → execute once → audit), extracted so that **any** agent can use it: a coding agent with `kubectl`, an Argo/CI bot, a LangGraph or MCP agent, or your own.

- **At the Kubernetes API.** An admission webhook sees agents' changes before they're stored, so an agent can't route around it. No agent code changes.
- **Bound to the exact change.** Approving "scale `checkout` to 3" admits that, once. It does not admit "scale to 0", a second restart, or the same change after someone reverted it.
- **Tamper-evident, and provable to others.** Every request, decision and execution is a signed link in a hash chain. `kubectl sentinel audit verify` checks it yourself, and also detects deleted records. `kubectl sentinel audit export` produces one evidence file that anyone can verify offline ([governance and compliance](../compliance/README.md)).
- **Small.** One Go binary, an 11.5 MB image (compressed). It needs no database: its state lives in the Kubernetes API, so three replicas share it.

> **Status: v2.0.0-alpha.3.** The design and the safety properties are tested on a real API server in CI (33 checks, below). Treat the API and the CRD (`v1alpha1`) as subject to change.

## Try it in one command (Docker, no cluster)
From a clone of the repository:

```bash
docker compose up
```

That builds and starts the guard and a support-desk agent working behind it. Open **http://localhost:8080** and sign in as Alice with her token `alice-demo` (Bob's is `bob-demo`):
- receipts go out on policy alone, and are recorded;
- each refund waits for you in the inbox: *Approve once* or *Deny*, with a note;
- closing an account needs two different people: Alice approves, then Bob;
- the agent can never delete customer data, so it hands those tickets to a person;
- now and then it reuses a spent capability, and the guard refuses (`docker compose logs -f agent`);
- every request that needs someone is announced to a webhook; `docker compose logs -f notifications` shows what Slack would receive.

The inbox opens on an **Overview**:
- what waits for people and for how long;
- what ran, what policy blocked, what people denied;
- whether the audit chain verifies;
- each agent's record, and the latest activity in plain sentences.

Click any request for its **details**: the exact change, what the guard observed, who approved, what ran, and its signed audit trail. Notifications link straight there. *History* verifies the chain and downloads the evidence bundle. Add `--profile monitoring` for Prometheus, loaded with the chart's alert rules (`localhost:9090`), and the Sentinel Guard dashboard in Grafana (`localhost:3000`). More in [examples/quickstart](../../examples/quickstart/README.md).

![The inbox's Overview: what waits, what ran, what was blocked or denied, the chain's health, each agent, and the activity](../handbook/images/screens/guard-overview.png)

### Or one container, driven with curl
The same guard runs as a single process with an HTTP API: an agent asks before acting, a human approves, and the agent gets a single-use capability that the executor redeems.

```bash
docker run --rm -p 8080:8080 -e SENTINEL_GUARD_AGENT_TOKENS=refund-bot=agent-secret \
  -e SENTINEL_GUARD_ADMIN_TOKEN=approver-secret ghcr.io/abdulmdev/sentinel-guard:2.0.0-alpha.3
```

The agent proposes an action. The guard answers `pending`, with a request id:

```bash
curl -s -X POST localhost:8080/v1/actions -H "Authorization: Bearer agent-secret" \
  -d '{"action":"refund","target":{"name":"order-1042"},"change":{"amount":"42.00 EUR"},"intent":"charged twice"}'
```

A human approves it (use the id from the previous answer):

```bash
curl -s -X POST localhost:8080/v1/actions/ar-af61fb76-1/approve -H "Authorization: Bearer approver-secret" \
  -d '{"approver":"alice@example.com","note":"duplicate charge confirmed"}'
```

The agent collects its capability (valid once, for 10 minutes):

```bash
curl -s localhost:8080/v1/actions/ar-af61fb76-1 -H "Authorization: Bearer agent-secret"
```

The executor (whatever performs the refund) redeems it just before acting. A second redemption gets `409`:

```bash
curl -s -X POST localhost:8080/v1/capabilities/spend -d '{"capability":"<token>","executor":"payments"}'
```

Instead of the curl approval, you can open http://localhost:8080 and decide in the inbox (with `approver-secret`).

Single-process mode keeps state in memory: it's for trying Guard out and for tests. In production, run it in Kubernetes.

## Install on Kubernetes
Requires Kubernetes 1.30 or newer (the webhook uses `matchConditions`).

```bash
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0-alpha.3 \
  -n sentinel-system --create-namespace
```

Or without Helm:

```bash
kubectl apply -f https://github.com/abdulmdev/sentinel-incident-copilot/releases/download/v2.0.0-alpha.3/install.yaml
```

Then install the kubectl plugin:

```bash
kubectl krew index add sentinel https://github.com/abdulmdev/sentinel-incident-copilot.git
```

```bash
kubectl krew install sentinel/sentinel
```

(Or download `kubectl-sentinel` for your platform from the [release](https://github.com/abdulmdev/sentinel-incident-copilot/releases) and put it on your `PATH`.)

**Which callers are agents?** By default, every ServiceAccount in the `agents` namespace. Change it with `--set agents.groups=...` or `--set agents.users=...` (for example `system:serviceaccount:ci:deploy-bot`). Only these identities are ever sent to the guard.

**Who may approve?** Anyone with the `sentinel-guard-approver` role in `sentinel-system`; for example `--set approvers.groups={sre-oncall}`. Agents can never approve, even with that role, and nobody can approve their own request.

## What it looks like
An agent tries to scale a deployment:

```
$ kubectl -n shop scale deploy/checkout --replicas=3        # as the agent
Error from server (Forbidden): admission webhook "workloads.guard.sentinel.dev" denied the request:
Sentinel Guard: scale of shop/deployment/checkout needs human approval (request ar-c8f4166c-1).
Approve with: kubectl sentinel approve ar-c8f4166c-1. Then retry the same change; it will be admitted once.
```

The on-call engineer looks and approves:

```
$ kubectl sentinel requests
ID             AGE  PHASE    AGENT          ACTION  TARGET                    CHANGE            RISK
ar-c8f4166c-1  12s  Pending  agents/fixer   scale   shop/deployment/checkout  replicas: 2 → 3   medium

$ kubectl sentinel approve ar-c8f4166c-1 -m "load spike, ok"
Approved ar-c8f4166c-1: scale of shop/deployment/checkout (replicas: 2 → 3), by alice.
Valid once, until 10:42AM. The agent's next identical attempt will be admitted.
```

The agent's retry is admitted; an identical retry after that needs a new approval. `kubectl sentinel describe <id>` shows the request, the guard's own precondition checks and the audit trail; `kubectl sentinel audit verify` checks the whole chain.

## What is guarded
By default, the webhook sees agents' **updates and deletions** of workloads and **deletions** of common objects:

| Kubernetes operation | Guard action | Notes |
|---|---|---|
| `kubectl scale`, a `replicas` change | `scale` | `replicas: 2 → 3`; policy limits (`minReplicas`, `maxReplicas`) block, they can't be approved |
| `kubectl rollout restart` | `restart` | kubectl's restart timestamp is not part of the binding, so an approved restart survives a retry |
| a container image change (incl. `kubectl rollout undo`) | `set-image` | shows `web=shop/web:v2 → web=shop/web:v1` |
| any other pod template change | `rollout` | bound to a digest of the new template |
| other spec changes, or several at once | `update` | |
| delete of a Deployment, StatefulSet, DaemonSet, ReplicaSet, Pod, Service, ConfigMap, Secret, PVC, ServiceAccount, Job, CronJob or Namespace | `delete` | |
| create (opt-in: `webhook.guardCreate=true`) | `create` | `observe` by default |

Server-side dry runs (`--dry-run=server`) are always admitted, with a warning saying what would happen. Metadata-only changes (labels, annotations) and status updates are not actions.

## Use it from your code, or with no code (Phase 12)
- **Python:** `cap = guard.require("refund", target="order-1042", change={"amount": "42.00 EUR"}, wait=300)`, then `guard.spend(cap.token)` right before acting. Or decorate the function with `@guard.guarded("refund", target="{order_id}")`. See [the Python SDK](../../sdk/python/README.md).
- **LangChain / LangGraph:** `guard_tool(delete_bucket, guard, target="{bucket}")` gives a guarded copy of any tool, which tells the model to wait for approval instead of acting.
- **TypeScript / Node:** the same API. See [the TypeScript SDK](../../sdk/typescript/README.md).
- **Any MCP server, no code:** put [`sentinel-guard-mcp-proxy`](../../examples/mcp-proxy/README.md) between your MCP client (Claude Desktop or Code, Cursor, an agent framework) and the server. Listed tools run only once approved; others pass through or are hidden.

Examples: [a refund agent that needs a human](../../examples/guarded-refunds/README.md), [the filesystem server behind the guard](../../examples/mcp-proxy/README.md).

Where the capability is redeemed decides what you're protected from:
- In the agent's own process (decorator, wrapper), it stops the model's mistakes and manipulation.
- In a separate executor, the MCP proxy, or the Kubernetes webhook, it also stops a compromised agent.

## Policy
The policy is YAML (the chart's `policy:` value; [the built-in default](../../guard/policies/default.yaml)). Each action gets a risk and an **autonomy** level:

| Autonomy | Meaning |
|---|---|
| `deny` | never allowed for agents |
| `approve` | a human approves each one (the default) |
| `auto` | allowed when **every precondition is observed** by the guard itself; otherwise a human approves |
| `observe` | allowed and recorded: shadow mode for rolling Guard out without blocking anything |

```yaml
protectedNamespaces: [kube-system, kube-public, kube-node-lease]   # agents may never change these
pendingTTL: 1h          # how long a request waits for a human
approvalTTL: 10m        # how long an approval stays usable
prometheus: {url: http://prometheus-server.monitoring.svc}
actions:
  scale:   {risk: medium, minReplicas: 1, maxReplicas: 20}
  delete:  {risk: high, autonomy: deny}
  restart:
    autonomy: auto
    preconditions:     # PromQL the guard runs itself; a non-empty result means "observed"
      - name: memory growth
        query: 'max(container_memory_working_set_bytes{namespace="{{.Namespace}}", pod=~"{{.Name}}-.*"}) > 300e6'
```

Preconditions are checked by the guard against your telemetry. An agent's own evidence never lets an action run on its own; an attacker can shape what an agent reads (for example through log lines), not what Prometheus measures. Target names are validated before they are put into a query, so a name can't rewrite it. When Prometheus is unreachable, a precondition is `unknown`, which never counts as observed.

## Two people, verified approvers, and notifications (Phase 16)
**Two-person rules.** Give an action `approvals: 2` (up to 5) and it needs that many *different* people:

```yaml
actions:
  delete:        {risk: high, approvals: 2}
  close-account: {risk: high, approvals: 2}
```

- In Kubernetes, `kubectl sentinel approve <id>` records your approval ("approval 1 of 2 … it needs 1 more, from someone else") and the request keeps waiting. The guard's webhook writes the approval as the identity the API server authenticated; nobody can write the approvals list by hand. The second person's `approve` decides it.
- Over HTTP and in the inbox, the same: one approval per person, the last one decides.
- The agent, its delegate, and anyone who already approved don't count. One denial ends the request.
- The capability lists every approver (`approvers`), the audit trail has a signed event for each, and the evidence verifier checks that an approved request shows the required number of distinct people.

**Approvers as themselves, outside Kubernetes.**
- **Personal tokens:** in single-process mode, `SENTINEL_GUARD_APPROVER_TOKENS=alice@example.com=…,bob@example.com=…` gives each person a token. Decisions are recorded as that person, and no shared admin token is generated.
- **OIDC:** with `SENTINEL_GUARD_OIDC_ISSUER` and `SENTINEL_GUARD_OIDC_AUDIENCE` (chart: `oidc.issuer`, `oidc.audience`), the guard verifies people's own OIDC tokens (Keycloak, Okta, Entra ID, Google…):
  - signature from the issuer's published keys; issuer, audience and expiry;
  - the person must be listed under `approvers` (users or groups).
- **Delegates:** a delegate (your console, a chat bot) can forward the person's token in `X-Sentinel-Person-Token`. The guard then records the person it verified, instead of taking the delegate's word.

**Notifications.** Set `SENTINEL_GUARD_NOTIFY_URL` (chart: `notify.existingSecret`) to a Slack or Discord incoming webhook, or any endpoint that reads JSON:

> Sentinel Guard: refund-bot asks to close-account barbara (customer: barbara, reason: customer request), high risk. Why: "ticket #1044: Barbara asked us to close their account". Approved by alice@example.com; needs 1 more, from someone else. Decide: http://localhost:8080/inbox/ (request ar-725ef9ba-1)

You're told when a request needs someone, when it still needs more approvals, and when one expires with nobody deciding. `SENTINEL_GUARD_PUBLIC_URL` sets the link; without it, the notice gives the `kubectl sentinel approve` command.

## The action API (agents outside Kubernetes)
| Method and path | Who | What |
|---|---|---|
| `POST /v1/actions` | agent | propose `{action, target, change, intent, dryRun}` → `200 allow` (with a capability) · `202 pending` · `403 block` |
| `GET /v1/actions/{id}` | the requesting agent | status; once approved, the single-use capability (issued once) |
| `POST /v1/capabilities/spend` | executor | verify and consume a capability atomically: `200`, or `409` if already used |
| `POST /v1/actions/{id}/approve` · `/deny` | the admin token (single-process mode) or a **delegate** | records `{approver, note}`; people in Kubernetes approve with `kubectl sentinel` |
| `GET /v1/audit/verify` | the admin token, or an **auditor** (policy `auditors`) | verify the audit chain |
| `GET /v1/audit/export` | the admin token, or an auditor | the evidence bundle: every request, the signed head, every policy version, the public keys |
| `GET /.well-known/jwks.json` | anyone | the guard's public key (capabilities are ES256 JWTs) |
| `GET /` (the approvals inbox) | a person with the admin token | single-process mode only: decide in a browser instead of with curl (below) |

In Kubernetes, agents authenticate with their ServiceAccount token (verified with a TokenReview); approvals are Kubernetes objects, so they follow your RBAC. A capability binds the request, action, target, the approved parameters (`change`) and approver, and expires with the approval.

**Approving from your own app (delegates).** If your product already signs people in (a console, a chat bot), it can record their decisions over HTTP as a *delegate*:
- list its identity under `delegates` in the chart values;
- call `POST /v1/actions/{id}/approve` with its own token and `{"approver": "alice@example.com"}`;
- the guard records Alice as the approver, via your app, so the trust is visible in the audit. Your app can never approve on behalf of the agent that asked.

Sentinel's incident console works this way. With `api.plainHTTP: true` the guard also serves this API inside the cluster over plain HTTP (no webhooks on that port).

## Evidence, policy versions and guardian alerts (Phase 14)
- **Export and verify.** `kubectl sentinel audit export > evidence.json` writes the whole record with your own RBAC (the viewer role is enough). `kubectl sentinel audit verify --bundle evidence.json --jwks keys.json` checks it anywhere, without cluster access:
  - every signature and link, and the signed chain head;
  - that each request's outcome and approver match its signed events;
  - that the rules each request names are in the file.
  
  Add `--previous last-month.json` to check that nothing recorded in an earlier, archived export was removed or changed since.
- **Policy versions.** Each request names the SHA-256 of the effective policy it was evaluated under. The guard keeps every version, signed, in the `sentinel-guard-policies` ConfigMap.
- **Self-verification.** Every replica verifies the whole chain every 5 minutes (`SENTINEL_GUARD_VERIFY_INTERVAL`) and exports the result as `sentinel_guard_audit_chain_ok`.
- **Alerts.** `--set prometheusRule.enabled=true` (with the Prometheus Operator) installs rules for:
  - a broken, unverified or rewound chain;
  - an approval backlog;
  - per-agent drift: bursts of blocked attempts, a rising share blocked or denied, people overriding an agent, an agent suddenly needing approval for what it used to do on policy.
  
  The rules are in [`alerts/guard-alerts.yaml`](../../deploy/helm/sentinel-guard/alerts/guard-alerts.yaml), unit-tested with `promtool`.
- **Dashboard.** `--set grafanaDashboard.enabled=true` adds the Sentinel Guard dashboard as a ConfigMap that Grafana's sidecar loads (label `grafana_dashboard: "1"`, as kube-prometheus-stack expects). It shows the chain's health, what waits for a person and for how long, each agent's outcomes and drift ratios, and firing guard alerts.
- **Auditors.** List identities under `auditors` (chart values) to let them call `GET /v1/audit/export`, for example a CronJob that archives evidence monthly.

What this maps to in the EU AI Act and the NIST AI RMF, and what stays yours: [docs/compliance](../compliance/README.md).

## How it works
- **Classification.** The webhook turns an admission request into an action and a *fingerprint* of the exact change (`replicas=3`, the new images, a digest of the new template). The approval is bound to agent + action + target + fingerprint.
- **Requests are `ActionRequest` objects** in `sentinel-system`. The guard alone creates or deletes them. An approver may set `spec.decision` once, and the guard's webhook stamps `decidedBy` with the identity the API server authenticated, not what the client wrote.
- **Exactly once.** Every state change is a compare-and-swap on the object's `resourceVersion`, so when replicas race, one wins. When the API server retries a write that conflicted, it runs admission again. The guard recognises the *identical resulting object at the same `metadata.generation`* within seconds and admits it again: re-applying an identical object changes nothing. A new attempt, or the same change after it landed and was reverted, is a new action.
- **Audit chain.** Each event (requested, approved, executed, …) commits to the previous one, to the request's content and is signed with the guard's ES256 key. The chain head lives in a ConfigMap and the public key is published in another. A place claimed by a replica that then lost a race becomes a *signed void*, so the chain stays complete. Nobody without the key can use a void to hide a deleted record.
- **High availability.** Three replicas spread across nodes and zones (PodDisruptionBudget). Every replica serves the webhook from a watch-fed cache; one replica (lease) does housekeeping: applying decisions, expiring requests, keeping the webhook CA bundle. On first start the guard creates its own TLS certificate and signing key, so installing needs no cert-manager and no secrets in manifests.

## When things fail
| Failure | Agents' changes | Everyone else |
|---|---|---|
| Guard pods down | refused ("failed calling webhook"): fail closed | **unaffected**: the API server doesn't call the guard for them |
| API server or etcd slow | slower, still exactly once | as before |
| Prometheus down | preconditions `unknown`: `auto` actions need a human | unaffected |
| A replica dies mid-write | the other replicas continue; an interrupted place in the chain is reported, never hidden | unaffected |

**Stopping every agent at once:** `kubectl -n sentinel-system scale deploy/sentinel-guard --replicas=0`. Agents' changes are refused while the guard is down, and everyone else carries on. Scale it back up to resume.

Emergency: a cluster admin can remove the `sentinel-guard` ValidatingWebhookConfiguration (break-glass, visible in the Kubernetes audit log).

## Measured
On a fresh kind cluster (1 node, laptop), 3 replicas, through the API server ([raw data](../../evals/guard/latency.json), [method](../handbook/17-lld-sentinel-guard.md#8-measured)):

| | p50 | p99 |
|---|---|---|
| A human's change (the guard is never called) | 2.5 ms | 6.1 ms |
| An agent's change that is waiting for approval | 3.8 ms | 7.5 ms |
| An agent's change after approval (admitted once) | 25.6 ms | 34.6 ms |

The end-to-end suite (`scripts/guard-e2e.sh`, run in CI) passes 33 checks against a real API server:
- agents wait while humans don't;
- an approval admits exactly that change, once, including across the API server's retries;
- a different change, a second restart, or the same change after a revert each need a new approval;
- scale to zero is blocked;
- agents can't approve;
- records can't be edited or deleted;
- dry runs preview;
- with the guard down only agents are stopped, and it is back within 2 s of scaling up;
- the audit chain verifies, the evidence export verifies offline, an edited export fails, and a later export extends an archived one;
- an action that needs two people waits after the first approval, refuses the same person twice, and is admitted once after a second person approves.

## Limits (alpha)
- Retention: `ActionRequest` objects are kept (they are the audit record), about 2 KB per request. Export and archive them (`kubectl sentinel audit export`); there is no compaction yet.
- The audit chain head is a single ConfigMap: every event is serialized through it. That's fine at human-approval rates, but it's not a high-volume event log.
- One cluster per guard. API-mode agents in single-process mode keep state in memory only.
- The inbox is for single-process mode. Give each person their own approver token, or verify their OIDC tokens; with the shared admin token the name is self-declared. The inbox has no OIDC sign-in of its own: people paste a token.
- The SDKs install from the repository (`pip install "sentinel-guard @ git+…"`) or from the release assets until they are published to PyPI and npm ([publishing](publishing.md)).
- Only the operations in the table above are guarded by default. Add more with `webhook.extraRules` (they're classified as `update` or `delete`).

More: the design and threat model in the [engineering handbook, chapter 17](../handbook/17-lld-sentinel-guard.md), and the decision record [ADR-0010](../adr/0010-sentinel-guard.md).
