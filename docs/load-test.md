# Load test results

`scripts/load-test.sh` (k6, [`deploy/k6/load.js`](../deploy/k6/load.js)) against `scripts/dev-local.sh up` on one MacBook Pro (M1 Pro, 32 GB).
Run 2026-10-05, **while a local 8B LLM benchmark was saturating the same machine**: treat latencies as conservative.
The agent ran the rule-based investigator (no LLM), so this measures the platform, not model latency.

| Scenario | Load | p95 latency | Errors | Threshold |
|---|---|---|---|---|
| Incidents: gateway → agent (LangGraph, Postgres checkpoints) → MCP servers → chaos-lab | 5 incidents/s for 60 s, 20 tenants | **314 ms** | 0% | p95 < 2 s ✓ |
| Hybrid search: embedding + pgvector + full-text + RRF | 20 req/s for 60 s, 30 tenants | **18 ms** | 0% | p95 < 300 ms ✓ |
| Rate-limit burst: one tenant, 200 concurrent requests, Redis Lua bucket (capacity 120) | 50 VUs × 4 | n/a | n/a | **120 admitted, 80 × 429 + Retry-After** ✓ |

1,701 requests total, 100% of checks passed. Raw k6 summary: [`load-test-results.json`](load-test-results.json).

## What it shows
- **Platform headroom.** Incident handling is dominated by I/O fan-out (6 tool calls, checkpoint writes), not CPU. With an LLM in the loop, model latency (seconds) dominates everything here (milliseconds), which is why the agent is async end to end.
- **The distributed limiter is exact under concurrency.** 200 simultaneous requests through several gateway threads admitted exactly the bucket capacity. That's the atomic Lua script working; a read-then-write limiter would over-admit.

## Not covered yet
- Multiple gateway replicas (the limiter is designed for it, and `RedisRateLimiterIT` tests two replicas sharing one budget).
- LLM-backed incidents under load. With a local 8B model on one GPU, requests serialize (~30 s per incident). This needs a hosted model or GPU serving to be meaningful.
