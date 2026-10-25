**Sentinel Guard 2.0.0-alpha.3: any agent, provable records, one command to try.** SDKs and a zero-code MCP proxy for agents outside Kubernetes; a signed evidence export anyone can verify offline; guardian alerts on agent drift; and `docker compose up` with a live agent and an approvals inbox.

**Try it**

```bash
git clone https://github.com/abdulmdev/sentinel-incident-copilot && cd sentinel-incident-copilot
docker compose up        # then open http://localhost:8080, token approve-me
```

**Install**

```bash
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0-alpha.3 -n sentinel-system --create-namespace
```

Or `kubectl apply -f install.yaml` (attached below). The kubectl plugin is attached too (`kubectl-sentinel_<os>_<arch>.tar.gz`), or install it with krew. The Python SDK (wheel) and the TypeScript SDK (npm tarball) are attached as well.

**New since alpha.2**
- **SDKs and the MCP proxy.**
  - Python and TypeScript SDKs: `require`, then `spend`, or a decorator.
  - A LangChain/LangGraph tool wrapper.
  - `sentinel-guard-mcp-proxy`, which puts the guard in front of any MCP server's tools with no code.
- **Evidence export.**
  - `kubectl sentinel audit export` (or `GET /v1/audit/export` for policy `auditors`) writes one self-verifying file.
  - `kubectl sentinel audit verify --bundle` checks it offline, against a pinned key (`--jwks`) and an archived export (`--previous`).
  - Each request now names the version of the policy it was decided under, and chain heads are signed.
- **Guardian monitoring.**
  - Every replica re-verifies the audit chain.
  - New metrics: per-agent outcomes, the approval backlog and chain health.
  - `prometheusRule.enabled=true` installs 8 alerts: integrity, oversight lapses, and drift in each agent's behaviour.
  - `grafanaDashboard.enabled=true` adds the Sentinel Guard dashboard.
- **An approvals inbox** in single-process mode (`docker run`, `docker compose`). Open the guard's URL to approve or deny in a browser, verify the chain, and download the evidence.
- **`sentinel-guard -healthcheck`,** for container health checks.
- **Fixed:** the CRD lacked `spec.decidedVia`, so delegated decisions lost it in Kubernetes mode.
- **The chart is now signed** with cosign, like the image.

**Measured**
- **Guard end to end on a real API server:** 31/31 checks, including tampered evidence and the guard taken down. p99 for an approved change admitted once: 40.8 ms (3 replicas).
- **Evidence:** verifying 1,000 requests (3,000 events) takes about 0.18 s; the bundle is about 2.1 KB per request.
- **The quickstart:** 18/18 checks in CI, with Prometheus and Grafana.
- **307 automated tests.**

**Upgrading from alpha.2:** requests recorded by older guards carry no policy version and their head is unsigned. Both still verify, with a note.

**Alpha:** the API and the CRD (`v1alpha1`) may change.

Docs: [docs/guard/README.md](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/guard/README.md) · design: [handbook chapter 17](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/handbook/17-lld-sentinel-guard.md) · governance: [docs/compliance](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/compliance/README.md) · decisions: [ADR-0012](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0012-sdks-and-the-mcp-proxy.md), [ADR-0014](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0014-governance-evidence-and-guardian-monitoring.md), [ADR-0015](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0015-quickstart-inbox-and-opt-in-publishing.md)
