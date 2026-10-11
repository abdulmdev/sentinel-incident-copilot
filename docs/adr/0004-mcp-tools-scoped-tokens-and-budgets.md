# ADR-0004: Agent tools over MCP, downscoped tool tokens, graph-enforced budgets

**Status:** accepted, 2026-10-05

## Context
In Phase 1 the agent called chaos-lab's HTTP endpoints straight from Python. That doesn't scale past a demo:
- Every new data source means agent code changes.
- The tools can't be reused by other clients (IDEs, Claude Desktop, other agents).
- The agent would need broad credentials.
- A model in a loop can spend without limit.

## Decision

**1. Tools are MCP servers.**
- `mcp-tools/observability` (Python, FastMCP) serves deploys, error logs and metrics.
- The gateway serves `search_runbooks` at `/mcp` (Spring AI MCP server).
- Both run **stateless** streamable HTTP: no MCP sessions, so any replica serves any call behind a plain load balancer.
- The agent discovers tools at runtime through `langchain-mcp-adapters`.

**2. The agent gets a downscoped token, never the user's.** When the gateway opens an incident it mints a JWT with:
- `sub=agent:<incident>`, `tenant_id`
- `scope=knowledge:read`, `aud=sentinel-mcp`
- a 15-minute expiry

Security rules:
- `/mcp` requires `SCOPE_knowledge:read`.
- Every other endpoint *rejects* tokens with `aud=sentinel-mcp`.
- A leaked tool token can't open, read or approve incidents.

The token travels in the `X-Sentinel-Tool-Token` header, lives only in LangGraph's `configurable`, and is never written to the checkpoint (a test asserts this).

**3. Tenant flows from the verified token into the tool, not through thread-locals.** The MCP transport's context extractor copies the tenant from the authenticated servlet request into the MCP transport context, which the tool handler reads. Tool execution can hop threads, so a `SecurityContextHolder` lookup inside the tool would be a latent cross-tenant bug.

**4. Budgets are enforced by the graph, not by the prompt.**
- **Tools:** `investigator ⇄ tools` is a LangGraph loop. The tools node runs at most `MAX_TOOL_CALLS` (8); calls past the cap get a `skipped` tool result and aren't executed.
- **Spend:** model spend is computed from real `usage_metadata` × the price table, and the loop stops at `MAX_COST_USD` ($0.50) before running the next batch.
- **Failures:** unknown tools, tool failures and LLM outages become error results or a scripted fallback, never a crashed incident.
- Every stop reason (`done | step_cap | cost_cap`) is recorded, exported as a metric and printed in the postmortem.

**5. Durable state in Postgres.** `AsyncPostgresSaver` checkpoints every step into a `langgraph` schema owned by the non-superuser app role (created by Flyway). A paused incident survives restarts and can be resumed by any replica. `test_postgres_checkpointer.py` proves this across two independent processes.

## Consequences
- + New data sources are new MCP servers. Agent code doesn't change.
- + Least privilege between services. The blast radius of a compromised agent is "read this tenant's runbooks for 15 minutes".
- + Bounded cost and latency per incident, with numbers to report.
- − `langchain-mcp-adapters` 0.3 needs `mcp<2`, so both Python services pin it. Revisit when the adapter supports mcp 2.x.
- − HS256 with a shared secret is a dev stand-in. Production would use an OIDC issuer with asymmetric keys, so services can verify tokens without being able to mint them.
- − Each tool step re-lists tools from both MCP servers (stateless, cheap at this scale). Cache the tool list per incident if it shows up in latency profiles.

## Lessons from running it live
- **Wire shape:** FastMCP sends a list-returning tool's result as one content block per item, which made one-item results ambiguous. Every observability tool now returns a single JSON object, enforced by `test_server.py`.
- **Grounding:** blindly citing the top search hit produced a wrong citation (`checkout-rollback` for a memory leak). A runbook is now cited only if it contains the exact signature that triggered the conclusion.
