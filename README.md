# Sentinel: an agentic incident-response copilot

> **Start here:** [engineering handbook](docs/handbook/README.md) (HLD, LLD, operations, history) · [system design](docs/DESIGN.md) · [demo script](docs/DEMO.md) · [benchmark report](evals/rca/REPORT.md) · [ADRs](docs/adr/)

Sentinel is an on-call copilot that closes the loop. When an alert fires, an incident opens by itself (deduplicated and correlated). A LangGraph agent collects evidence (deploys, error logs, metrics, runbooks), ranks root-cause hypotheses with citations, and proposes a remediation. **Nothing runs until a human approves it.** Approval mints a **single-use capability** for exactly that action, which a deploy-tool adapter executes once. The agent then **verifies recovery** with real traffic, records the **time to mitigate**, and writes the postmortem.

```
 Prometheus ─▶ Alertmanager ─(integration token)─┐
            JWT (user)                           ▼        LangGraph, checkpointed in Postgres
 console ──────────────▶ gateway-service ────────────────▶ agent-service
                         Spring Boot 4 + Spring AI 2      investigator ⇄ tools ─▶ triage ─▶ rca ─▶ ⏸ approval
                         · authN, tenancy, rate limits      (≤ 8 tool calls, ≤ $0.50)              (human)
                         · alerts: dedupe + correlate                      ─▶ execute ─▶ verify ─▶ postmortem
                         · /ask = RAG + Claude                 │ MCP (streamable HTTP, stateless)    │
                         · /mcp  search_runbooks ◀─────────────┤ tool token: tenant, knowledge:read  │ capability:
                         · mints tool tokens + capabilities    ▼                                     │ one action, once, 5 min
                           │            ▲                 mcp-observability ─▶ chaos-lab ◀── mcp-remediation
          upload doc ──────┘            │ hybrid search   deploys · logs · metrics · probe      (Redis: spent jtis)
                ▼                       │ (pgvector + FTS, RRF, RLS)
     Kafka knowledge.documents ──▶ ingestion-service ──▶ Postgres
                                   chunk → embed (local ONNX) → atomic replace; owns all Flyway migrations
```

## Why it's built this way

| Decision | Reason |
|---|---|
| Java gateway + Python agents | Platform concerns (auth, tenancy, rate limits, resilience) sit on the JVM. The agent ecosystem (LangGraph) is in Python. See [ADR-0001](docs/adr/0001-polyglot-gateway-and-agents.md). |
| Human-in-the-loop interrupt | Remediation changes production. LangGraph `interrupt()` pauses the graph, and the gateway takes the reviewer's identity from the JWT, not the request body. |
| Model routing | Triage is classification, so it runs on `claude-haiku-4-5`. RCA needs reasoning, so it runs on `claude-opus-5-5`. |
| Heuristic fallback | Without an API key, or when an LLM call fails, rule-based triage and RCA run instead. They are also the **eval baseline** the LLM path has to beat. |
| Tenant from the token, everywhere | The gateway ignores `tenant_id` in request bodies. Agent threads are keyed `tenant:incident`. Postgres uses row-level security. |
| chaos-lab | Real services with injectable faults, so the agent is tested on real symptoms instead of mocked text. |
| Hybrid retrieval in Postgres | Vectors and full-text live in the same row and are fused with RRF. One system, one transaction, one RLS model. See [ADR-0003](docs/adr/0003-hybrid-retrieval-in-postgres.md). |
| Tools over MCP | Observability and runbook search are MCP servers the agent discovers at runtime, reusable by any MCP client. See [ADR-0004](docs/adr/0004-mcp-tools-scoped-tokens-and-budgets.md). |
| Downscoped tool tokens | The agent never holds a user's token: the gateway mints a 15-minute, single-tenant, `knowledge:read`, MCP-only JWT per incident, and it's never checkpointed. |
| Capabilities, not deploy credentials | The agent holds no standing power to change production. Approval mints a 5-minute JWT bound to one action, one service and one incident. The adapter verifies every claim and spends its jti in Redis before acting, failing closed. A compromised agent can at most do the one thing a human approved, once. See [ADR-0008](docs/adr/0008-closed-loop-capabilities-and-alert-intake.md). |
| Alerts open incidents | Alertmanager webhook with a per-tenant integration token. Re-fires are deduplicated, and several rules tripping on one fault are correlated into one incident under a per-service advisory lock. `opened_at` = the alert's start. |
| Zero-trust identity | Only the gateway can mint service credentials: ES256 with a published JWKS, so the agent, MCP servers and remediation adapter verify but can't forge. Users sign in through OIDC (Keycloak in dev). Every hop is authenticated and tenant-bound, and the agent's checkpoints are under Postgres RLS. CI checks which pod may hold which secret. See [ADR-0009](docs/adr/0009-zero-trust-identity.md). |
| Verified, measured outcomes | "Resolved" means probes pass after the fix, not "someone clicked approve". Time to mitigate is recorded per incident and as a histogram. |
| Budgets enforced by the graph | Tool-call and dollar caps are checked in code before each step. Stop reasons are recorded, exported as metrics and printed in the postmortem. |
| Durable incidents | Every step is checkpointed to Postgres, so a paused incident survives restarts and resumes on any replica. |
| Guardrails: model proposes, policy disposes | PII redaction and injection removal on tool output; citation validation, runbook policy interlocks and constrained decoding on model output. Measured raw vs. guarded in the benchmark. See [ADR-0006](docs/adr/0006-guardrails-and-production-hardening.md). |
| Pluggable LLM | `SENTINEL_LLM_PROVIDER=anthropic\|ollama`: Claude or a free local model (gemma4) for both the agent and `/ask`. |
| Distributed rate limits | Atomic Lua token bucket in Redis using Redis's clock; agent traffic gets its own bucket; per-instance fallback if Redis is down. |
| Semantic cache for `/ask` | pgvector, tenant-scoped, invalidated by a knowledge-version fingerprint the moment a runbook changes. |
| End-to-end tracing | One OpenTelemetry trace per incident across gateway → agent → MCP servers, with GenAI attributes (model, tokens, cost). Grafana dashboard for cost, safety and latency. |
| Async ingestion over Kafka | Uploads return once Kafka acks them. Embedding runs in ingestion-service, re-ingest is idempotent (content hash), and bad messages go to a dead-letter topic. |

## Results

From [`evals/rca/REPORT.md`](evals/rca/REPORT.md), 100 replayed incidents, deterministic scoring:

| Run | Remediation accuracy | Harmful actions | Root cause |
|---|---|---|---|
| Rule baseline v1 | 76% | 16% | 76% |
| Rule baseline v2 (deploy suspect window, found by the eval) | 92% | **0%** | 92% |
| v2 on held-out set (unseen seed) | 92% | 0% | 92% |
| Local LLM (gemma4 8B, Ollama) | pending: `evals/rca/run.py --mode llm --provider ollama` (~60 min, resumable) | | |
| Claude investigator | pending: needs `ANTHROPIC_API_KEY` | | |

The remaining baseline misses are metrics-only diagnoses with the log backend down: the gap the LLM path has to close.

**Closed loop, live (Phase 7):** fault injected → Prometheus alert → incident opened automatically → approved in the console → rollback executed via capability → verified healthy. **Time to mitigate 21–33 s** (alert start → verified, including the human decision). A second fault after the rollback opened exactly one incident with the correct action. The same flow passes on Kubernetes (kind) in CI.

## Run it

Prerequisites: Java 21+, Maven, [uv](https://docs.astral.sh/uv/), Docker.

Fast local loop (infra in Docker, services as local processes, no image builds). Sign in at http://localhost:3000 with Keycloak (dev users `alice@acme` (tenant admin), `bob@acme` and `carol@globex`; their test passwords are in `deploy/keycloak/sentinel-realm.json`) or the dev login. Console at http://localhost:3000, traces at http://localhost:16686, dashboard at http://localhost:3001, alerts at http://localhost:9093. Break checkout with `python3 chaos-lab/inject.py checkout bad_deploy` and the incident appears by itself:

```bash
scripts/dev-local.sh up
```

With a local LLM via [Ollama](https://ollama.com) (no API key):

```bash
SENTINEL_LLM_PROVIDER=ollama scripts/dev-local.sh up
```

On Kubernetes (local kind cluster, the real Helm chart). Console at http://localhost:13000:

```bash
scripts/kind-up.sh
```

AWS: `deploy/terraform/aws` (EKS, RDS Postgres + pgvector, MSK, ElastiCache, ECR), then `helm install` with `values-aws.yaml`. See [ADR-0007](docs/adr/0007-deployment-kubernetes-and-aws.md).

Load test (k6, three scenarios incl. rate-limit correctness):

```bash
scripts/load-test.sh
```

Or everything in containers (the `observability` profile adds Prometheus alerting, Alertmanager, loadgen and Grafana):

```bash
cd deploy && docker compose --profile observability up --build
```

```bash
./scripts/demo.sh bad_deploy
```

Load the runbooks, then measure retrieval quality:

```bash
python3 scripts/seed_knowledge.py acme
```

```bash
python3 evals/retrieval/run.py
```

Other faults: `latency`, `errors`, `memory_leak`, `db_pool_exhaustion`. Set `ANTHROPIC_API_KEY` (see `.env.example`) to use the LLM path. Without it, Sentinel runs the heuristic baseline.

### Tests and evals
```bash
cd gateway-service && mvn verify
```

```bash
cd ingestion-service && mvn verify
```

```bash
cd agent-service && uv run pytest
```

```bash
cd mcp-tools/observability && uv run pytest
```

```bash
cd mcp-tools/remediation && uv run pytest
```

- `mvn test` runs fast unit tests. `mvn verify` adds the `*IT` integration tests against a real pgvector database (Testcontainers, needs Docker).
- The agent's durability test runs when `SENTINEL_TEST_PG_DSN` is set (see `tests/test_postgres_checkpointer.py`).
- Cross-service payloads are pinned in [`contracts/`](contracts/README.md).
- RCA benchmark with its regression gate (offline, ~1 s): `uv run --project agent-service python evals/rca/run.py --check`
- CI: [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs all of the above on every PR.

## Roadmap

- [x] **Phase 1, foundation:** monorepo, compose stack, gateway (JWT, tenancy, rate limit, retries), LangGraph workflow with an approval interrupt, chaos-lab, tests
- [x] **Phase 2, ingestion + RAG:** Kafka → heading-aware chunking → local embeddings → pgvector + Postgres full-text, RRF hybrid search, a Spring AI `RetrievalAugmentationAdvisor` on `/ask` with [n] citations, a retrieval eval
- [x] **Phase 3, agents:** MCP tool servers (Python + Spring AI), downscoped tool tokens, LLM investigation loop with step and cost caps, Postgres checkpointer (survives restarts), runbook-grounded RCA citations, contract fixtures
- [ ] **Phase 2b:** cross-encoder reranking
- [x] **Phase 4, evals:** 100 replayed incidents (8 failure families × 5 difficulty tiers) + held-out set, deterministic metrics (remediation/cause accuracy, harmful-action rate, citation faithfulness, grounding), CI regression gate, GitHub Actions. See [evals/README.md](evals/README.md)
- [x] **Phase 4b:** local-LLM column (gemma4 via Ollama) in the benchmark
- [x] **Phase 5, hardening:** guardrails (PII, prompt injection, citation validation, policy interlocks, constrained decoding) with an injection benchmark tier; Redis distributed rate limiting; `/ask` semantic cache; OpenTelemetry tracing end to end; Grafana dashboard; k6 load test
- [x] **Phase 6, ship:** Next.js console (BFF, httpOnly sessions), incident system of record, hardened images, Helm chart verified on a local Kubernetes cluster (migrations hook, NetworkPolicies enforced), Terraform for AWS (EKS, RDS, MSK, ElastiCache), CI for images/chart/infra/kind e2e, [design doc](docs/DESIGN.md), [demo script](docs/DEMO.md)
- [x] **Phase 7, close the loop:** Alertmanager → incidents (deduplicated, correlated per service); capability-gated execution through an MCP deploy-tool adapter (single-use, fails closed); post-execution verification; time to mitigate; least-privilege secrets per pod; [ADR-0008](docs/adr/0008-closed-loop-capabilities-and-alert-intake.md)
- [x] **Phase 8, zero-trust identity:** gateway-only ES256 service tokens + JWKS, Keycloak OIDC login (code + PKCE), gateway → agent and agent → MCP authentication, revocable integration tokens, checkpoint RLS, per-pod secrets checked in CI; [ADR-0009](docs/adr/0009-zero-trust-identity.md)
- [ ] **Next:** Claude benchmark column (needs a key); KMS-backed signing and mTLS; async approval with streamed progress; dependency-aware correlation; incidents recorded from chaos-lab and real postmortems
