# ADR-0017: The whole picture in one place, and no stuck incidents

**Status:** accepted, 2026-10-25. Builds on [ADR-0015](0015-quickstart-inbox-and-opt-in-publishing.md) (the inbox), [ADR-0014](0014-governance-evidence-and-guardian-monitoring.md) (the oversight summary) and [ADR-0011](0011-copilot-approvals-through-the-guard.md) (the copilot's approvals through the guard).

## Context
Using the system end to end, as a newcomer would, showed three gaps:
- **The inbox was only a queue.** It listed what waited for a decision, but not what agents had been doing, what was blocked, or whether the record was healthy. Seeing that meant opening Grafana. Nothing showed a single request's full story: what it asked, what the guard observed, who decided, what ran, and the signed events.
- **An incident could get stuck for good.** The copilot records a reviewer's decision in the guard before resuming the agent. When the guard no longer had the request, the decision failed and the incident stayed *Awaiting approval*. That happens when the request expired, or when a single-process guard restarted and its memory went with it. Its alert links stayed active, so every later alert on that service was correlated into the stuck incident: no new investigation ever started.
- **An approval could be answered "pending".** In Kubernetes, a replica answers from its watch cache, which trails writes made through another replica by milliseconds. An approver who retries right after approving, or the load generator's "inline" round, could be told the request still waits. This was seen once in CI.

## Decisions
1. **An Overview, built from the record.**
   - `GET /v1/overview` (approvers, the admin token, auditors) returns:
     - the same oversight summary as the evidence bundle (`Summarize` over the signed events);
     - what waits now and the oldest wait;
     - unspent approvals;
     - each agent's outcomes, with its waiting count and when it was last seen;
     - the policy version;
     - the guardian monitor's last self-verification.
   - It doesn't re-verify on each call, so the inbox can poll it.
   - The inbox opens on an Overview tab: tiles, a per-agent table, and an activity feed that turns signed events into sentences ("alice@example.com gave one approval for close-account on ada").
2. **A details view for every request**, built from data the inbox already has: the change, the guard's observations, every approval and the decision, the capability, the policy version, and the signed audit trail with hashes.
   - It opens from cards, the activity feed, history, and a link: `…/inbox/#<request id>`.
   - Notifications now link straight to the request.
   - Rendering stays text-only under the same CSP.
3. **A rejection never needs the guard.** If the guard answers 404 or 409 to a rejection (the request is gone, expired or already decided), the gateway closes the incident anyway; a rejection runs nothing. An approval stays strict: refused, with a message saying how to get unstuck.
4. **Alerts never join an incident that has ended.** Before linking an alert, the gateway releases any link still pointing at an incident that is resolved, closed or failed, however it ended. So the next alert opens a new incident.
5. **A fresh read before "pending".** When the request the guard is about to answer "pending" for has no decision in the cache, the engine re-reads that one request from the source of truth (`Refresher`, implemented by the Kubernetes store). The cost is one API GET, only on the answers that keep an agent waiting.

## Consequences
- One page answers "what are my agents doing, and is anything wrong?", and a click answers "what exactly happened here?". Grafana stays for history and alerts.
- The demo, and real deployments with a single-process guard, can't wedge an incident or swallow later alerts.
- The pending path makes one extra read in Kubernetes. Its latency was re-measured (chapter 17 §8).
- The details view shows what the record holds; verifying the chain is still one click in History, or offline with the evidence bundle.
