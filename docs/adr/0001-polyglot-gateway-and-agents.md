# ADR-0001: Java gateway, Python agent service

**Status:** accepted, 2026-10-05

## Context
Sentinel needs two kinds of software. One is platform plumbing: auth, multi-tenancy, rate limiting, retries and observability. The other is agent orchestration: stateful graphs, checkpoints, human interrupts and fast iteration on prompts and tools.

## Decision
- **gateway-service** (Spring Boot 4, Spring AI 2) owns the public API, JWT validation, tenant resolution, per-tenant rate limits, retries with timeouts, and simple single-call LLM features (`/ask`).
- **agent-service** (FastAPI, LangGraph, LangChain) owns the incident workflow. It is internal only and trusts the gateway for identity.
- They talk over HTTP/JSON. Opening an incident is idempotent on `(tenant, incident id)`, so the gateway can retry network failures safely.

## Consequences
- + Each side uses the strongest ecosystem for its job. This mirrors how many large companies split platform and ML teams.
- + The agent-service can be scaled and deployed on its own (LLM latency dominates its load profile).
- − Two runtimes to build, test and operate. The gateway↔agent contract has to be tested (contract tests are planned for Phase 3).
- − One extra network hop. That's negligible next to LLM latency (seconds).
