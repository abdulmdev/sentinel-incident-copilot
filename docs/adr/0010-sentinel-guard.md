# ADR-0010: Sentinel Guard: the safe-action core as a Kubernetes-native service in Go

**Status:** accepted, 2026-10-23 (extracts the capability design of [ADR-0008](0008-closed-loop-capabilities-and-alert-intake.md) and [ADR-0009](0009-zero-trust-identity.md) into a product of its own; first release `v2.0.0-alpha.1`)

## Context
Sentinel's core is approval → single-use capability → execute once → audit. It works, but it is welded to incident response: the gateway mints the capability, and only Sentinel's own remediation adapter checks it. Meanwhile:
- More and more agents act on infrastructure: coding agents with `kubectl`, CI and GitOps bots, agents built on LangGraph or MCP.
- Teams cite security and risk as the top barrier to deploying agents, and "guardian" layers that enforce at run time are becoming a category.
- Most guardrail tools check inside the agent's own process, where an agent (or whoever controls it) can route around the check.

Goal for v2: any agent, unmodified, can be held to "nothing changes unless policy or a human approved exactly this, once". The tool must be easy for an ordinary developer to adopt: `docker run`, `helm install`, `kubectl apply`, a kubectl plugin.

## Decisions

### 1. A separate service, enforcing at the Kubernetes API
- **Enforcement point: a validating admission webhook.** It sits in front of the API server for agents' updates and deletes of workloads, deletes of common objects, and opt-in creates. The agent needs no code change and can't skip the check: the API server calls the guard before storing anything.
- **Only agents are sent to the guard.** The webhook's `matchConditions` (CEL, Kubernetes ≥ 1.30) select agent identities (groups or users from the chart values). That makes it safe to **fail closed**: with the guard down, agents are refused while humans, CI and controllers are never called out to.
- **An HTTP action API for everything outside Kubernetes**, with the same engine: propose → approve → single-use capability → the executor spends it.

### 2. Go, not the Java/Python stack of v1
The guard is on the request path of every agent change and is shipped to strangers. One static binary gives an 11.5 MB image (compressed, measured), instant start, and a single-file kubectl plugin from the same code. Go is also the language of the Kubernetes tooling this is installed next to (Kyverno, Gatekeeper, cert-manager). The v1 services are unchanged; they'll call the guard over HTTP when the copilot moves onto it.

### 3. State in the Kubernetes API, not a database
*This deviates from the v2 plan, which had a Flyway `V8__action_log` table.*
- **Requests are `ActionRequest` custom resources** in the guard's namespace. The audit chain head and the public keys are ConfigMaps; the signing key and the TLS certificate are Secrets the guard creates on first start.
- **The API server is the compare-and-swap.** Every write carries `resourceVersion`, so when replicas race to consume one approval, exactly one write succeeds. Proven with 50 concurrent attempts (unit test) and on a real API server (e2e).
- **Nothing to operate.** No Postgres or Redis to install, back up or fail over. The guard is as available as the API server it protects; when the API server is down, nothing can change anyway.

### 4. Approvals are Kubernetes objects, decided through RBAC
- An approver sets `spec.decision` (`kubectl sentinel approve`, or a plain `kubectl patch`). Who may do so is ordinary RBAC: the chart ships a `sentinel-guard-approver` role.
- **The guard's mutating webhook stamps `decidedBy`** with the identity the API server authenticated, overwriting anything the client wrote.
- It refuses decisions by agent identities and by the requester, refuses edits to what was requested, and refuses creating or deleting requests by anyone but the guard (even cluster-admin).

### 5. Bind approvals to the exact change; recognise API-server retries
- **The binding** is agent + action + target + a *fingerprint* of the change: `replicas=3`, the new images, or a digest of the new template. kubectl's restart timestamp is excluded, so an approved restart survives a retry.
- **API-server retries** were found in the e2e load test: the API server re-runs admission when it retries a conflicting write, which burned approvals. The guard re-admits a write only if it is the **identical resulting object at the same `metadata.generation`** within 10 s, at most 5 times.
  - Re-applying an identical object can't change anything twice.
  - A change that landed and was then reverted has moved the generation on, so it needs a new approval. Found by the same load test.
  - Scale requests carry no generation, so the guard reads the workload's.

### 6. A global, signed hash chain, with signed voids
- **Every event is a link in one chain:** it commits to the previous event, to the request's content (a digest) and is signed with the guard's ES256 key. Verification (by the kubectl plugin, from the published public key) detects edits, reordering and deletions, which show as gaps.
- **The event's place is claimed before the single state-and-event write.** A replica that then loses a race records the place as a *signed void* rather than leaving a gap. A transition is checked against current state *before* claiming, so refused attempts (for example a replayed capability) write nothing.

### 7. Shared watch cache, one housekeeper
- Every replica reads from a watch-fed cache (informer) and writes with compare-and-swap. One replica, holding a Lease, applies decisions, expires requests and keeps the webhook CA bundle.
- Before this, three replicas' housekeeping loops ran in lockstep and raced on 30 of 50 approvals. Since then, the load run produces zero voids, and the approved path went from p99 266 ms to p99 under 50 ms (chapter 17 has the measurements).

## Consequences
- **Positive:**
  - Unmodified agents can be governed with one `helm install`.
  - A guard outage stops only agents.
  - The audit trail is verifiable by anyone with read access.
  - There is no database to run.
- **Negative:**
  - The chain head is a single ConfigMap: events are serialized cluster-wide. That's fine at human-approval rates; it is not a high-volume event log.
  - `ActionRequest` objects accumulate (no retention or export yet).
  - Only the configured operations are guarded.
  - The HTTP API's single-process mode keeps state in memory.
- **Follow-ups:**
  - Move the incident copilot's approvals onto the guard.
  - SDKs and an MCP proxy.
  - Retention with signed export.
  - Slack approvals.
  - Chapter 16 tracks these.
