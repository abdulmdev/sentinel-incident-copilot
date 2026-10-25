**Sentinel 2.0.0: Sentinel Guard, the safe-action layer for any AI agent.** No agent changes production unless policy, or the right people, approved that exact change, and then it runs once. Every decision is signed and provable to others. Sentinel's own incident copilot runs on it.

**Try it in one command**

```bash
git clone https://github.com/abdulmdev/sentinel-incident-copilot && cd sentinel-incident-copilot
docker compose up        # then open http://localhost:8080 as Alice (token alice-demo) or Bob (bob-demo)
```

**Install on Kubernetes**

```bash
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0 -n sentinel-system --create-namespace
```

Or `kubectl apply -f install.yaml` (attached below). The kubectl plugin is attached too (`kubectl-sentinel_<os>_<arch>.tar.gz`), or install it with krew. The Python SDK (wheel) and the TypeScript SDK (npm tarball) are attached as well. The image and the chart are signed with cosign (keyless).

**What's in v2**
- **Enforcement at the Kubernetes API:**
  - an admission webhook holds agents' changes (coding agents with `kubectl`, CI bots, any agent) with no code changes;
  - an approval admits exactly that change, once, including across the API server's own retries;
  - humans, CI and controllers are never slowed down;
  - if the guard is down, only agents are stopped.
- **Any agent, anywhere:**
  - Python and TypeScript SDKs;
  - a LangChain/LangGraph tool wrapper;
  - a zero-code MCP proxy that puts the guard in front of any MCP server's tools.
- **People decide, as themselves:**
  - in Kubernetes with `kubectl sentinel approve`;
  - elsewhere with personal tokens or OIDC tokens the guard verifies;
  - in an inbox with an Overview of every agent's actions and the full story of each request.
  - The riskiest actions can require two or more different people (`approvals: N`).
  - Notifications go to Slack-style webhooks.
- **Provable to others:**
  - every request, decision and execution is a signed link in a hash chain;
  - `kubectl sentinel audit export` produces one evidence file anyone can verify offline, against a pinned key and an archived export;
  - every replica re-verifies the chain, and tested alert rules flag drift in each agent's behaviour;
  - mapped to the EU AI Act and the NIST AI RMF ([docs/compliance](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/compliance/README.md)).
- **SafeActionBench,** an open benchmark for harmful agent actions. An independent guard alone took a small model from 33.9% harmful to 0%.

**New since 2.0.0-alpha.3**
- Two-person approvals, approvers verified by OIDC, delegates forwarding the person's token, and notifications.
- The inbox's Overview and per-request details, with deep links.
- An incident whose guard request is gone can be closed. Alerts never join an incident that has ended.
- A fresh read before "pending", so an approval made a moment ago through another replica counts at once.
- **The API is stable: `sentinel.dev/v1`.** `v1alpha1` is still served, deprecated.

**Upgrading from an alpha**

```bash
kubectl apply -f https://raw.githubusercontent.com/abdulmdev/sentinel-incident-copilot/v2.0.0/deploy/helm/sentinel-guard/crds/actionrequests.sentinel.dev.yaml
helm upgrade sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0 -n sentinel-system
```

`helm upgrade` never updates CRDs. A 2.0.0 guard on the old CRD stops, and its log says exactly what to apply. Records from the alpha releases are read through v1 and keep verifying, and requests pending across the upgrade can still be approved. CI tests this path from 2.0.0-alpha.3 on a real API server.

**Measured**
- **On a real API server:** 33/33 end-to-end checks, and the upgrade test.
- **Latency (p99, 3 replicas, through the API server):**
  - an approved change admitted once: 34.6 ms;
  - an agent change waiting for approval: 7.5 ms;
  - a human's change: 6.1 ms.
- **The `docker compose` quickstart:** 29/29 checks in CI.
- **329 automated tests.**

Docs: [docs/guard/README.md](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/guard/README.md) · design: [handbook chapter 17](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/handbook/17-lld-sentinel-guard.md) · decisions: [ADR-0010](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0010-sentinel-guard.md) to [ADR-0018](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0018-a-stable-api-for-v2.md)
