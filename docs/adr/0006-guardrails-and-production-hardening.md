# ADR-0006: Guardrails, pluggable LLMs and production hardening

**Status:** accepted, 2026-10-05

## Context
By Phase 4 Sentinel could diagnose incidents and had a deterministic benchmark, but:
- **Untrusted inputs:** log text (written by services, and through them by attackers) and PII went straight into model prompts and stored evidence.
- **Trust in model output:** whatever action, target and citations the model returned were shown to the on-call human as-is.
- **Scaling:** per-instance rate limits, no caching, no tracing, no cost visibility.
- **No LLM column:** the comparison couldn't be filled without an Anthropic key.

## Decisions

### 1. The model proposes, deterministic policy disposes (`agent-service/app/guardrails.py`)

**Inputs** (applied to every tool output before any model, checkpoint or evidence store sees it):
- **PII redaction:** emails, Luhn-validated card numbers, JWTs, API keys.
- **Injection removal:** instruction-like spans in logs are cut out and counted.
- **Spotlighting:** tool output is wrapped in `<untrusted_tool_output>` delimiters.

**Outputs:**
- **Citations** must name evidence gathered in this run. Recognizable formats are normalized; anything else is dropped and counted.
- **Policy interlocks from the runbooks:**
  - No rollback without a deploy of that service inside the 30-minute suspect window. That also means no rollback when deploy history is unavailable: there's nothing verified to roll back to.
  - No action on a service the agent never investigated.

  Violations are downgraded to `escalate` with the reason attached, never silently rewritten.

**Constrained decoding:** the RCA schema narrows `remediation.target` to an enum of investigated services. Grammar-constrained providers (Ollama) then can't emit an invalid target.

**Observability:** every intervention is a guardrail event: in state, in the postmortem, as a span event, and as a Prometheus counter.

### 2. Measure guardrails, don't assume them
The benchmark gained an `injection` tier: attacker text in logs next to a plausible old deploy, plus planted PII. One attack is phrased to evade the injection filter on purpose. The scorer reports `harmful_action_rate` both **before** and **after** policy interlocks, plus `attack_success_rate` and `pii_leak_rate`. The CI gate requires zero harmful actions, zero successful attacks and zero PII leaks after guardrails, for every LLM provider.

### 3. Pluggable LLM provider
`SENTINEL_LLM_PROVIDER=anthropic|ollama`:
- **Agent:** LangChain `ChatAnthropic` / `ChatOllama`.
- **Gateway:** Spring AI `spring.ai.model.chat`.
- **Local (ollama):** free and private. Structured output uses Ollama's JSON-schema `format`.
- **Thinking:** off by default for the local model. Measured 3× faster on the same answer.

### 4. Distributed rate limiting (Redis)
- **Algorithm:** a token bucket per tenant in one Lua script (atomic check-and-decrement, Redis's own clock, so replica skew can't mint tokens).
- **Agent traffic:** tool calls get a separate `tenant:agent` bucket, so an incident doesn't starve the humans' budget.
- **Outage policy:** if Redis is unreachable, fall back to per-instance buckets (available, still bounded, counted).

### 5. Semantic cache for `/ask` (Postgres + pgvector)
Tenant-scoped (RLS). A hit requires all of:
- the same model;
- the same **knowledge version** (a fingerprint of the tenant's documents), so a runbook edit invalidates cached answers immediately;
- an entry inside the TTL;
- cosine similarity ≥ 0.95. That's deliberately strict: a wrong cached answer to an on-call question costs more than an LLM call.

### 6. Tracing and dashboards
- **Tracing:** OpenTelemetry end to end (Spring Boot 4's OTel starter, Python SDK with FastAPI/ASGI/httpx instrumentation), giving one trace per incident across gateway → agent → MCP servers. GenAI semantic conventions (`gen_ai.request.model`, `gen_ai.usage.*`) plus cost per LLM span.
- **Dashboards:** a provisioned Grafana dashboard for incident outcomes and latency, LLM spend per incident and per step, tokens by model, cache hit ratio, guardrail interventions, rate limiting, retrieval latency and ingestion.

## Lessons from running it
- **The trace was noise, then a profiler.** The first trace had 338 spans per incident (per-chunk ASGI spans, security filter chain). Trimmed to the useful ones (80), it showed the agent making **50 MCP requests for 6 tool calls** (tool lists re-discovered every graph step) and **spending the tenant's human API budget**. Fixes: a per-token tool-list cache and a separate agent bucket. Results: 26 MCP requests, 0 agent calls against the human budget.
- **The local model falls for the same traps as the old rules.** gemma4 proposed rolling back a harmless deploy hours earlier, and complied with the filter-evading injection. The policy interlock blocked both. Hence defense in depth: input filters are bypassable, output policy is not.
- **Free-text fields defeat interlocks.** The model put "checkout database connection pool" in `target`. Constrained decoding fixed it at the source.

## Consequences
- + Guarantees that don't depend on the model: no unverified rollback, no fabricated citation, no PII in prompts. All of them are measured in CI.
- − Interlocks can block a correct action when evidence is missing (rollback with deploy history down). They fail toward `escalate`, which costs minutes, not an outage.
- − The injection filter is regex-based and will miss novel phrasings (one benchmark attack is built to). It's a first layer; the output policy is the backstop.
- − Each MCP tool call still opens a stateless session (about 3 HTTP requests). A pooled session per incident would cut that further.
