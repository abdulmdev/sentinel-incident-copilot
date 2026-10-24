**Sentinel Guard 2.0.0-alpha.2: one safety core.** Sentinel's own incident copilot now runs on Sentinel Guard. The agent proposes, the console's approvals are recorded in the guard, and the remediation adapter redeems the guard's single-use capability.

**Install**

```bash
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version 2.0.0-alpha.2 -n sentinel-system --create-namespace
```

Or `kubectl apply -f install.yaml` (attached below); the kubectl plugin is attached too (`kubectl-sentinel_<os>_<arch>.tar.gz`), or via krew. Try it without a cluster: `docker run -p 8080:8080 ghcr.io/abdulmdev/sentinel-guard:2.0.0-alpha.2`.

**New in alpha.2**
- **Delegated decisions.** A service that signs people in (a console, a chat bot) can record their approvals over HTTP. List it under `delegates`; the decision is recorded as the person's, *via* that service, and the audit shows both. A delegate can never approve for the agent that asked.
- **Approved parameters travel with the capability.** Capabilities now carry `namespace` and `change` (the approved parameters), so executors check that they run exactly what was approved (e.g. a pool size of 40, not 60).
- **An in-cluster action API port** (`api.plainHTTP`, `SENTINEL_GUARD_API_ADDR`) without the admission webhooks, for clients in other namespaces.
- **The copilot on the guard** (Sentinel 0.7.0 chart): the guard is a dependency of the `sentinel` chart. The agent and gateway get their own ServiceAccounts and short-lived projected tokens; a NetworkPolicy covers the guard.

**Measured** (live, local stack):
- **Closed loop:** alert → incident in 27 s → rollback approved in the console → verified healthy, **time to mitigate 24.4 s**.
- **Audit:** the guard's chain verified, with the request by the agent, the approval by the reviewer via the gateway, the capability issued, and the execution by the adapter.
- **Guard down:** with the guard stopped, an approval returned 503 and nothing ran.
- **Tests and CI:** 252 automated tests. CI ran the copilot end to end on Kubernetes through the guard, plus the guard's 25 checks.

**Alpha:** the API and the CRD (`v1alpha1`) may change.

Docs: [docs/guard/README.md](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/guard/README.md) · design: [handbook chapter 17](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/handbook/17-lld-sentinel-guard.md) · decisions: [ADR-0010](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0010-sentinel-guard.md), [ADR-0011](https://github.com/abdulmdev/sentinel-incident-copilot/blob/main/docs/adr/0011-copilot-approvals-through-the-guard.md)
