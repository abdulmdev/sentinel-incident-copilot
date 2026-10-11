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
| CI | Workflow never ran on GitHub (repo not pushed) | Unknown runner-specific failures | Push and iterate |
| Data | No retention or deletion policy for incidents, the cache, checkpoints | Unbounded growth; data-protection obligations | TTL jobs; tenant deletion workflow |
| Product | Alertmanager only (no PagerDuty/Opsgenie) | Teams on other tools open incidents by hand | Further receivers on the same `AlertService` |

## 2. Roadmap (proposed order)

![Roadmap items by impact and effort](images/16-roadmap-quadrant.png)

*Figure 16.1: Roadmap items by impact and effort*

1. **Fill the LLM columns** (gemma4 full run; Claude when a key is available) and record them in chapter 11.
2. **Push to GitHub**, make CI green, publish images.
3. **Real-world benchmark data:** record chaos-lab incidents; import anonymized postmortems.
4. **Identity, next:** KMS-backed signing; mTLS/SPIFFE between pods; egress NetworkPolicies.
5. **Closed loop, next:** async approval with streamed progress; dependency-aware alert correlation; closed-loop scenarios in CI; a real deploy-system adapter with mandatory dry run.
6. **Scale and cost:** pooled MCP sessions, reranker, slimmer images, apply Terraform in a sandbox.

*Done in Phase 7 (removed from this list):* alert webhooks, and remediation execution behind interlocks with capability tokens, verification and audit.
*Done in Phase 8:* OIDC users, asymmetric (ES256) service tokens with a JWKS, gateway ↔ agent service identity, checkpoint RLS, revocable integration tokens.
