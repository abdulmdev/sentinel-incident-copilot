# 16 Limitations and roadmap

Honest list of what's missing or weak, with the risk and the intended fix. Remove items when they're done (and note it in chapter 15).

## 1. Known limitations

| Area | Limitation | Risk | Planned fix |
|---|---|---|---|
| Evaluation | LLM columns (gemma4, Claude) not yet measured on the full set | Claims about LLM quality are anecdotal (pilot only) | `run.py --mode llm --provider ollama` (~60 min, resumable); Claude with a key and `--max-usd` |
| Evaluation | Generator and labels share one author | Benchmark blind spots invisible | Incidents recorded from chaos-lab and real postmortems; externally written red-team cases |
| Evaluation | Retrieval eval has 20 questions | Mode differences not significant | Grow to ~100; add a cross-encoder reranker and measure |
| Auth | The gateway's ES256 private keys live in a Kubernetes Secret, not a KMS/HSM | A compromised gateway pod can read them | Sign through AWS KMS (asymmetric `ECC_NIST_P256`) so the key never leaves the HSM |
| Auth | No mTLS between pods | Tokens authenticate, but in-cluster traffic is plaintext | Service mesh (Linkerd/Istio) or SPIFFE identities, in addition to the tokens |
| Auth | Dev issuer (HS256) still exists for scripts, CI and kind | Misconfigured production could accept dev tokens | Off by default; `check_chart_secrets.py --prod` fails if the dev secret reaches any pod |
| Security | Regex injection filter | Novel phrasings get through (by design, the interlock is the backstop) | Classifier-based detection; more red-team cases |
| Security | No egress NetworkPolicy | Wider blast radius if a pod is compromised | Egress allow-lists; mTLS (service mesh) or a service token gateway → agent |
| Agent | Each MCP tool call opens a stateless session (~3 HTTP requests) | Latency and request overhead | A pooled MCP session per incident |
| Agent | The rule baseline can't diagnose from metrics alone (8 benchmark misses) | Escalation where a fix was possible (safe, slower) | Left for the LLM path to close; measure |
| Remediation | The approval request is synchronous across execution and verification (up to about a minute) | Slow UI response; a gateway restart mid-call loses the response (the agent still finishes and the record catches up on the next read) | Return 202 at approval and stream progress (SSE) from the agent's checkpoints |
| Remediation | The adapter fronts the chaos-lab ops API only | Real deploy systems (Argo CD, Spinnaker, a cloud API) need their own adapters | Same MCP tool contract per backend; `dry_run` first in production |
| Remediation | No automatic rollback of a failed remediation; no auto-approval | A wrong fix waits for a human (safe, slower) | Per-tenant auto-approval for low-risk actions gated on measured precision; compensating actions |
| Alerts | Correlation is per service | A cascade (checkout errors caused by inventory) still opens two incidents | Correlate along the service dependency graph within a time window |
| Alerts | Integration tokens are bearer credentials (≤ 90 days) | A leak allows forged alerts for that tenant until revoked (investigations are read-only and rate-limited) | Short TTLs plus automated re-issue; or mTLS from Alertmanager |
| Evaluation | The benchmark scores *proposals*; execution and verification are tested by unit, integration and live runs, not by the 112-incident set | The TTM claim rests on two live scenarios | Replay closed-loop scenarios (fault → alert → approve → verify) in CI with the lab, recording TTM per fault type |
| Observability | ingestion-service and mcp-remediation aren't traced (the adapter has a JSON audit log); no log aggregation; alert rules exist for the lab services but not SLOs for Sentinel itself | Gaps when debugging ingestion | OTel in both; Loki; SLOs for incident latency, error rate and TTM |
| Images | Java images ~1.2 GB (ONNX natives for all platforms + model) | Slow pulls/scale-up | Platform-specific natives, jlink, a model layer shared via a volume or init container |
| Deployment | Terraform validated, never applied; ESO/ALB/OTel Collector assumed | Unknown apply-time issues | Apply in a sandbox account; add the add-ons via Helm/Terraform |
| Data | No retention or deletion policy for incidents, the cache, checkpoints | Unbounded growth; data-protection obligations | TTL jobs; tenant deletion workflow |
| Product | Alertmanager only (no PagerDuty/Opsgenie) | Teams on other tools open incidents by hand | Further receivers on the same `AlertService` |
| Guard | `ActionRequest` objects are never deleted (they are the audit record) | Unbounded growth of objects in `sentinel-system`; the watch cache holds them all | Retention with a signed export (the chain segment, its head and the public key), then compaction |
| Guard | The chain head is one ConfigMap: every event is serialized through it, and `voids` grows with lost races | Throughput suits human approvals, not a high-volume event log; voids accumulate slowly | Segment the chain per period with signed checkpoints; compact voids into the export |
| Guard | Signing key and TLS cert in Kubernetes Secrets; rotating the key drops the old public key | Events signed with an old key can't be verified after rotation | Keep a key history in the JWKS ConfigMap; optional KMS signing |
| Guard | Only the configured operations are guarded (workload updates and deletes, deletes of common objects); `exec`, `port-forward`, CRDs from other operators and RBAC changes are not, by default | An agent with broad RBAC can still act through unguarded paths | Grow the default rules; pair with least-privilege RBAC for agent ServiceAccounts; Kyverno/Gatekeeper packs |
| Guard | The incident copilot (v1) still mints its own capabilities | Two approval paths in one repo | Move `IncidentService.decide` and the remediation adapter onto the guard's API (the next v2 step) |
| Guard | Approvals only via `kubectl sentinel` / the API; no Slack or console UI; single-process mode keeps state in memory | Approvers must use the terminal | Slack interactive approvals; a console page; a durable single-process store |

## 2. Roadmap (proposed order)

![Roadmap items by impact and effort](images/16-roadmap-quadrant.png)

*Figure 16.1: Roadmap items by impact and effort*

0. **Sentinel Guard, next** (v2): move the incident copilot onto the guard; SDKs and an MCP proxy for agents outside Kubernetes; retention with signed export; Slack approvals; Kyverno/Gatekeeper packs; the SafeActionBench benchmark.
1. **Claude column** (needs an API key); improve the local model's accuracy on red-herring and injection tiers (currently 33–38%: safe, but escalated).
2. **Real-world benchmark data:** record chaos-lab incidents; import anonymized postmortems.
3. **Identity, next:** KMS-backed signing; mTLS/SPIFFE between pods; egress NetworkPolicies.
4. **Closed loop, next:** async approval with streamed progress; dependency-aware alert correlation; closed-loop scenarios in CI; a real deploy-system adapter with mandatory dry run.
5. **Scale and cost:** pooled MCP sessions, reranker, slimmer images, apply Terraform in a sandbox.

*Done in Phase 7 (removed from this list):* alert webhooks, and remediation execution behind interlocks with capability tokens, verification and audit.
*Done (Phase 8):* pushed to GitHub with CI green and images published.
*Done in Phase 8:* OIDC users, asymmetric (ES256) service tokens with a JWKS, gateway ↔ agent service identity, checkpoint RLS, revocable integration tokens.
