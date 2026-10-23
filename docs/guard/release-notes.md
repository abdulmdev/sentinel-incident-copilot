**Sentinel Guard: envelope protection for AI agents.** No agent changes your cluster unless policy, or a human, approved that exact change, and then only once. Humans, CI and controllers are never sent to the guard.

This is the first (alpha) release of Sentinel v2: Sentinel's safety core, extracted so that any agent is held to it, without code changes.

**Install**

```bash
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0-alpha.1 -n sentinel-system --create-namespace
```

Or `kubectl apply -f install.yaml` (attached below). The kubectl plugin is `kubectl-sentinel_<os>_<arch>.tar.gz` below, or via krew:

```bash
kubectl krew index add sentinel https://github.com/abdulmdev/sentinel-incident-copilot.git && kubectl krew install sentinel/sentinel
```

Try it without a cluster: `docker run -p 8080:8080 ghcr.io/abdulmdev/sentinel-guard:2.0.0-alpha.1`.

**What's in it**
- **Enforcement at the Kubernetes API:**
  - a validating webhook, sent only agent identities (`matchConditions`), which fails closed for them;
  - approvals as `ActionRequest` objects, decided through RBAC with `kubectl sentinel approve`;
  - the approver is stamped from the authenticated identity.
- **Exactly once:**
  - an approval is bound to agent + action + target + the exact change;
  - consumed by compare-and-swap;
  - the API server's own write retries are recognised (identical object, same generation).
- **Tamper-evident audit:** a signed hash chain, verifiable by anyone with read access (`kubectl sentinel audit verify`), which also detects deleted records.
- **An HTTP action API** for agents outside Kubernetes: propose → approve → a single-use ES256 capability → the executor spends it.
- **Operations:** 3 replicas, no database, self-provisioned TLS and signing key, a watch cache, one housekeeping lease.
- **Supply chain:** a multi-arch image (amd64, arm64), signed with cosign (keyless), with SBOM and provenance.

**Measured** (fresh kind cluster, 3 replicas, p99 through the API server):
- a human's change: 4.9 ms;
- an agent's pending change: 4.6 ms;
- an approved change admitted once: 42.9 ms.

The 25 end-to-end checks pass on a real API server.

**Alpha:** the API and the CRD (`v1alpha1`) may change. Known limits are in the docs (retention, the single chain head, only the configured operations are guarded).

Docs: [docs/guard/README.md](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/guard/README.md) · design: [handbook chapter 17](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/handbook/17-lld-sentinel-guard.md) · decision record: [ADR-0010](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0010-sentinel-guard.md)
