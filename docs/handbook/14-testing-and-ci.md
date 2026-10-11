# 14 Testing and CI

**206 automated tests** (measured 2026-10-11, end of Phase 8):

| Suite | Tests |
|---|---|
| gateway | 94 (68 unit/slice + 26 integration) |
| agent | 64 (incl. 9 parametrized cases and 3 Postgres tests: durability + checkpoint RLS) |
| mcp-remediation | 14 |
| eval scorer | 12 |
| mcp-observability | 12 |
| ingestion | 10 (7 + 3) |

Plus:
- the console production build (type check);
- two benchmark gates (main + held-out);
- Helm lint and strict schema validation for 3 overlays, and `terraform validate`;
- `promtool check rules` and `amtool check-config` for the alerting config;
- `scripts/check_chart_secrets.py` (who may hold which secret, production and kind values);
- k6 load thresholds;
- a **scripted live closed-loop scenario** (section 4).

## 1. Strategy

![Test layers, from unit tests up to the benchmark gate](images/14-test-layers.png)

*Figure 14.1: Test layers, from unit tests up to the benchmark gate*

![Tests by suite](images/14-chart-tests-by-suite.png)

*Figure 14.2: Tests by suite*

![Test count at the end of each phase](images/14-chart-tests-by-phase.png)

*Figure 14.3: Test count at the end of each phase*

- **Unit and slice tests** for logic and HTTP behavior (fast, no Docker).
- **Integration tests on real infrastructure** (`*IT`, Testcontainers: `pgvector/pgvector:pg16`, `redis:7.4-alpine`) wherever correctness depends on the database: SQL, RLS, vector search, Lua scripts. Mocks can't prove RLS.
- **Real tokens, real filter chains** for security tests (minted HS256 tokens through Spring Security, not mocked authentication).
- **Contract fixtures** shared by producer and consumer tests.
- **Fakes with production wire shapes**: the agent's `FakeToolBox` uses JSON-schema args and the same outputs as the MCP servers (a `**kwargs` fake once silently dropped all arguments). `ScriptedChatModel` drives the LLM loop deterministically.
- **The benchmark** is the system-level test of decision quality.

Run:
```bash
cd gateway-service && mvn verify          # mvn test = unit only
cd ingestion-service && mvn verify
cd agent-service && uv run pytest         # + SENTINEL_TEST_PG_DSN=… for the durability test
cd mcp-tools/observability && uv run pytest
cd mcp-tools/remediation && uv run pytest
uv run --project agent-service pytest evals/rca
uv run --project agent-service python evals/rca/run.py --check
```

## 2. What each suite proves

### gateway-service
| Test | Proves |
|---|---|
| `GatewayApplicationTest.contextLoads` | Full context with no mocks (caught the missing RestClient starter) |
| `IncidentControllerTest` (15) | 401 without a token; **tenant from the token, not the body**; payload equals the shared contract; reviewer = JWT subject; 403 without a tenant claim; agent 409 passthrough; validation; **agent outage → FAILED record**; decision recorded with the reviewer; list scoped to the tenant. Phase 7: **approval mints a capability bound to the proposed action/target/reviewer and passes it to the agent**; rejection and approved `escalate` mint nothing; execution audited with the capability's jti and verification result (and the alert link released); a failed remediation is audited and keeps the alert linked |
| `RemediationTokensTest` (2) | ES256, gateway `iss`; capability claims (aud `sentinel-remediation`, scope, tenant, incident, action, target, approver, 5-minute expiry); a fresh jti per approval |
| `AlertServiceTest` (7) | New alert → incident dated at the alert's `startsAt`, tenant from the token (a `tenant_id` label is ignored), severity mapped, investigated in the background; re-fire → deduplicated; **a second rule on the same service joins the open incident without a new investigation**; missing/invalid `service` label skipped; resolution recorded; fingerprint fallback is a stable label hash |
| `AlertSecurityTest` (9) | Real signed tokens: the gateway-issued integration token works on the webhook and its tenant is used; **an HS256-forged integration token → 401; a revoked one → 403**; anonymous → 401; **user token, a user token claiming `alerts:write` without the audience, and an agent tool token → 403**; the integration token can't read incidents or call `/mcp`; **a remediation capability is useless at the gateway** |
| `ClosedLoopRepositoryIT` (9) | V6 on real Postgres as `sentinel_app`: **8 concurrent deliveries of one alert open exactly one incident (no spare records)**; **6 different rules tripping on one service concurrently share one incident**; other services and other tenants get their own; a finished incident releases the alert for a new one; the incident is reported resolved only when its *last* alert clears; a resolution arriving after the incident closed still reaches it (not older ones); timeline and time-to-mitigate mirrored; audit is tenant-scoped |
| `IncidentRepositoryIT` (2) | Lifecycle (proposed action preserved across updates), per-tenant listing, FAILED kept |
| `TracePropagationTest` | Real `AgentClient` against a stub server receives W3C `traceparent`, **a verifiable 60-second agent-call token for the tenant**, **and** the tool token |
| `KnowledgeControllerTest` (4) | Upload publishes with the token's tenant and key `tenant:docId`; Kafka failure → 503; search scoped; bad mode/k → 400 |
| `KnowledgeSearchIT` (5) | Keyword OR-semantics on natural questions; stemming; hybrid ranks the right runbook per scenario; **never returns another tenant's chunks in any mode**; stop-word-only queries → empty |
| `ReciprocalRankFusionTest` (3) | Exact RRF scores; scale-independence; limits |
| `TenantDocumentRetrieverTest` | Fails closed without a tenant |
| `AskControllerTest` (2) | Grounded prompt contains numbered excerpts and the injection warning; citations returned; cache stored; **a cache hit calls neither the model nor retrieval** |
| `SemanticCacheIT` (3) | Paraphrase hit / different-question miss; a knowledge change invalidates; no cross-tenant or cross-model hits |
| `McpEndpointTest` (5) | Tool listed; tool call scoped to the token's tenant; user tokens can't call `/mcp`; tool tokens can't call `/api`; anonymous rejected |
| `ToolTokensTest` (2) | ES256 with `kid`; claims (sub, tenant, scope `knowledge:read telemetry:read`, aud, expiry); expired tokens rejected |
| `SigningKeysTest` (6) | First key signs, all published; `activeKid` pins the signer during rotation; the published set has no private parts; public-only keys and unknown kids rejected; production refuses an ephemeral key; dev gets one |
| `RoutingJwtDecoderTest` (8) | Own ES256 tokens verify. **Rejected:** a different EC key with our issuer and `kid`; **HS256 claiming our issuer** (algorithm confusion: the dev secret can't forge a capability); unsigned (`none`); own issuer without a service audience; dev tokens when the dev issuer is off, and dev tokens carrying a service audience; unknown issuers. **OIDC:** RS256 user tokens verify against a stub IdP JWKS; wrong audience or wrong key rejected |
| `JwksEndpointTest` | `/.well-known/jwks.json` is public, EC P-256 with `kid`, no `d`; the issuer metadata points to it |
| `IntegrationTokensIT` (3) | Real Postgres (V7, RLS) through the full filter chain: **issue (admin) → use on the webhook (202) → revoke → 403**; non-admins can't issue; another tenant's tokens are invisible and can't be revoked (404); expired records are inactive |
| `TenantRateLimiterTest` | Exhaust, per-tenant isolation, refill |
| `RedisRateLimiterIT` (4) | **Two replicas share one budget under concurrency (exactly 10 of 60)**; tenant isolation; refill; Redis outage → local fallback, counted |

### ingestion-service
| Test | Proves |
|---|---|
| `MarkdownChunkerTest` (7) | Heading paths; sibling headings replace deeper ones; bounded size with overlap; oversized paragraphs hard-split; `#hashtag` isn't a heading; empty docs; constructor validation |
| `IngestionPipelineIT` (3) | Index → unchanged → replace (old chunks gone); **RLS hides other tenants and unscoped reads** (connected as the non-superuser role); invalid documents rejected |

### agent-service
| Test file | Proves |
|---|---|
| `test_incident_flow.py` (13) | Scripted investigation's tool sequence, rollback, grounded citations; runbooks cited only when they name the signature; the runbook query is built from symptoms; runbook text never triggers rules; **tool token used but never persisted**; works without a token; a failing tool is recorded and policy escalates; approval → execution → verification → postmortem; rejection; decisions single-use; duplicate open 409; tenants isolated; **after a rollback, a new DB-pool incident is not rolled back again** (live-run regression) |
| `test_remediation.py` (8) | Approval executes with the capability and verifies: RESOLVED, ordered timeline, time-to-mitigate; no capability → nothing executes (REMEDIATION_FAILED); a refused capability (already used) → failure, not success; **a fix that doesn't heal is caught by verification** after the configured attempts; an action the adapter can't perform fails with a clear error; approved `escalate` → ESCALATED with no capability use; rejection executes nothing; **the capability is never checkpointed** |
| `test_investigation_budget.py` (5) | Step cap (exactly 8 executed); cost cap (stops before the second batch); clean finish; hallucinated tool → error result; LLM outage → scripted plan |
| `test_guardrails.py` (12) | PII kinds incl. a Luhn check; **metric floats never redacted**; injection removed and counted, ordinary ops text untouched; citations normalized/dropped; rollback window; uninvestigated target blocked; free-text target mapped; RCA schema enum; escalation always passes; **a rollback record or a rolled-back build doesn't justify another rollback**; "no recent deployments" doesn't satisfy the interlock |
| `test_heuristics.py` (7) | Deploy suspect window boundaries (5, 30, 31, 360, −5 minutes); old deploy → no rollback; unparseable time stays a suspect |
| `test_llm_providers.py` (2) | Ollama: local, free, no key, schema passed as `format`; Anthropic routing and pricing |
| `test_toolbox_cache.py` (2) | Discovered once per token, never shared across tokens; TTL and LRU |
| `test_tracing.py` (3) | One trace continuing the caller's trace id; node/tool spans; GenAI attributes; guardrail span events |
| `test_contracts.py` (1) | The gateway's payload fixture parses and matches the model's fields exactly |
| `test_auth.py` (8) | Gateway → agent: no token → 401; a token for the tenant → accepted; **another tenant's token → 403 in path and body**; tool-token audience, forged key, expired, and HS256 shared-secret tokens → 401; health and metrics open |
| `test_postgres_checkpointer.py` (3) | Durability across processes (now with a capability, so it resolves); **checkpoint RLS**: a request bound to another tenant reads nothing and can't write (`InsufficientPrivilege`), an unbound request reads nothing; tables FORCE RLS even for their owner |

### mcp-observability (12)
Discovery over an MCP client session; log grouping; metric parsing; unknown service → tool error; invalid level; single-object wire shape; `probe_service` success rate and p95; **timeouts count as failures and requests are capped at 50**; unknown services rejected; **`/mcp` requires a gateway tool token** (none, wrong audience, missing `telemetry:read`, or a forged key → 401); health stays open.

### mcp-remediation (14)
The capability check (`tests/test_capabilities.py`):
- an approved action executes once, and **the replay is refused without reaching the service**;
- wrong action, wrong target, wrong audience, wrong scope, expired (5 cases) → refused before any ops call;
- a missing token or a forged signature (attacker EC key with the same `kid`) → refused;
- **a capability forged with the old HS256 shared secret → refused** ("only ES256"), and one from another issuer → refused;
- `dry_run` validates without executing **or consuming** the capability;
- **replay store down → refused (fails closed)**;
- the pool size is bounded by the runbook; `scale_out` is bounded (≤ 10 replicas) and needs its own capability.

### Eval scorer (12)
Each metric's definition, including the edge cases: wrong escalation isn't harmful; a runbook-only conclusion isn't grounded; `unknown` isn't scored for grounding; targets matter when labeled; a crashed run is a miss; policy override is scored both ways; a dropped citation is unfaithful; PII detection in transcript or evidence; the offline BM25 index finds the right runbook.

## 3. CI (`.github/workflows/ci.yml`)

![CI pipeline: job dependencies](images/13-ci.png)

*Figure 14.4: CI pipeline: job dependencies*

| Job | Runs | Gate |
|---|---|---|
| `java` (matrix: gateway, ingestion) | `mvn -B verify` (Testcontainers) | Tests |
| `python` (matrix: agent, mcp-observability, mcp-remediation) | `uv run --frozen pytest`; a Postgres service + role/schema setup enables the durability test | Tests |
| `eval-gate` | Scorer tests; **benchmark regenerates byte-for-byte**; `run.py --check` on main and held-out; uploads `REPORT.md` | Metrics vs `thresholds.json` |
| `docs` | `scripts/check_docs.py`: every Markdown link resolves; every Flyway migration in chapter 08; every env var read by code in chapter 13; every metric in chapter 12 | Docs in sync with code |
| `web` | `npm ci && npm run build` | Build + types |
| `deploy-config` | `helm lint` (both charts); `helm template \| kubeconform -strict` for defaults/kind/aws; **`check_chart_secrets.py`** (`--prod`: signing keys only in the gateway, no dev secret anywhere; kind: no signing material outside the gateway/web); `terraform fmt -check`, `init -backend=false`, `validate` | Validity + least privilege |
| `kind-e2e` (needs the above) | kind cluster; `scripts/kind-up.sh`; seed knowledge; inject a fault; open an incident → expect `AWAITING_APPROVAL`; **approve → expect `RESOLVED`** (capability → remediation adapter → verification, in-cluster); **assert the console pod can't reach the agent or the remediation adapter** | End to end + NetworkPolicy |
| `images` (main only; needs kind-e2e, eval-gate) | Build jars + fetch the model; multi-arch (amd64/arm64) buildx; push `ghcr.io/<owner>/sentinel-<name>:<sha>`; GHA cache | — |
| `llm-eval` (manual `workflow_dispatch`) | `run.py --mode llm --max-usd 80 --check` with the `ANTHROPIC_API_KEY` secret (skips with a warning if absent) | Claude thresholds |

Status: the workflow is validated as YAML and every step's command has been run locally, but the workflow has **not executed on GitHub yet**: the repository hasn't been pushed.

## 4. Live closed-loop scenario (Phase 7)

A script drove the local stack (`scripts/dev-local.sh up`) through the real alert path, with no manual incident creation:

1. `inject.py checkout bad_deploy`;
2. the loadgen's traffic produces the error ratio;
3. the Prometheus rule `HighErrorRatio` fires;
4. Alertmanager posts to the webhook with the integration token;
5. an incident opens itself;
6. the scenario approves (as `alice@acme`): the capability is minted, the adapter rolls back, the probe passes, and the incident is `RESOLVED`;
7. then `db_pool_exhaustion` on the same service: **exactly one** incident, proposing `increase_pool` → approve → `RESOLVED`.

Measured (2026-10-06, rule baseline):
- alert → incident: 39–53 s, dominated by the rule's `for: 30s` and the 1-minute rate window;
- proposal ready 6 s after the alert's `startsAt`;
- **time to mitigate 21 s and 33 s** (alert start → verified healthy, including the human decision).

Both alert resolutions were later stamped on their incidents. While the gateway was down, Alertmanager retried and delivered once it came back.

The first run of step 7 failed, which is why it's in the suite now. See the Phase 7 bugs in [chapter 15](15-phase-history.md#phase-7-close-the-loop).

## 5. Adding tests: rules
- New DB behavior → a Testcontainers IT that connects as `sentinel_app` (not the container superuser).
- A new endpoint → security tests with real tokens (allowed and denied).
- A new agent behavior → a fake with production shapes; if it affects decisions, re-run the benchmark.
- A new privileged action → a capability test for each claim it binds (action, target, audience, expiry, replay).
- Update the counts in this chapter.
