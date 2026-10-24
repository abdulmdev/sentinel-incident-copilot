# ADR-0012: SDKs and an MCP proxy, so any agent can use Sentinel Guard

**Status:** accepted, 2026-10-24. Builds on [ADR-0010](0010-sentinel-guard.md) (the guard) and [ADR-0011](0011-copilot-approvals-through-the-guard.md) (the copilot on it).

## Context
The admission webhook guards agents that act through the Kubernetes API with no code changes, but most agents act elsewhere:
- payments, cloud APIs and databases;
- files, through MCP servers;
- tools in LangGraph/LangChain.

The HTTP action API (propose → approve → capability → spend) covers them, but every team would have to write the same client, polling, error handling and argument binding. Adoption depends on making the right thing the easy thing.

## Decisions

### 1. Two thin SDKs with the same contract: Python and TypeScript
- **The same surface in both:** `propose`, `status`, `require(…, wait)`, `spend`, and a `guarded` decorator or wrapper. Python also has `AsyncGuard` for asyncio.
- **Errors mean "don't act":**
  - `ApprovalPending` carries the request id to show a human;
  - `ActionBlocked`, `CapabilityRejected` and `GuardUnavailable` all fail closed. An unexpected status (e.g. 401 for a bad token) is `GuardUnavailable`, never treated as permission.
- **One canonical change encoding.** Strings stay as they are; anything else becomes compact JSON with sorted keys.
  - The approval is bound to the fingerprint of these values, so a mismatch would silently stop an approval from matching.
  - The first versions differed (Python wrote `{"a": 1}`, JavaScript `{"a":1}`). A golden value is now asserted by both test suites.
- **The decorators bind the approval to all of the call's arguments by default.** Narrowing it is an explicit choice (`change=…`).
- **Tested against the real guard binary** (built once per test run, memory mode), not mocks. The SDKs are only as right as their contract with the actual API.
- **No Java SDK yet.** The gateway's `GuardClient` is the reference for one.

### 2. A guarded LangChain tool talks to the model, not just the program
`guard_tool(tool, guard, target=…)` returns a copy of any LangChain tool (so any LangGraph agent's tools) with the same name and arguments.
- **Allowed** → the capability is redeemed and the tool runs.
- **Not allowed** → the model receives a plain explanation instead of a result ("Not done yet: … needs human approval (request ar-…). Tell the user …"). Raising an exception would end the agent's turn or be swallowed; a message lets the agent tell its user and retry the same call after approval.

### 3. An MCP proxy: zero-code adoption for MCP agents
`sentinel-guard-mcp-proxy --config proxy.yaml` is an MCP server (stdio) that fronts any MCP server (stdio or streamable HTTP). Claude Desktop/Code, Cursor and agent frameworks point at the proxy instead of the server.
- **Per tool:** `pass` (unchanged), `guard` (proposed with all its arguments; forwarded once allowed and redeemed), or `deny` (hidden from the tool list and refused). Unlisted tools follow `default`.
- **Upstream results pass through unchanged** (returned as the upstream `CallToolResult`, structured output included). Input validation is left to the upstream server, so the proxy never rejects what the server would accept.
- **Refusals are error results** with the reason, so the client never mistakes them for success.

### 4. Where the capability is redeemed decides what you're protected from

| Pattern | Who redeems | Protects against |
|---|---|---|
| SDK decorator, `guard_tool` | the agent process itself | the model's mistakes and manipulation (prompt injection, a wrong plan): it can only do what a human approved, once. **Not** a compromised process, which could skip the check |
| SDK `spend` in a separate executor (a payments service, an adapter) | the executor | also a compromised agent: it holds no power to act |
| MCP proxy | the proxy, a separate process the agent talks to | also an agent that can't reach the upstream server except through the proxy (deployment's job) |
| Kubernetes webhook | the API server, via the guard | also a compromised agent: enforcement is at the API server |

The docs state this table plainly, rather than implying that the in-process decorator stops a compromised process.

## Consequences
- **Positive:**
  - A guard-protected action is about three lines in Python or TypeScript, one wrapper for LangChain/LangGraph, or a config file for MCP.
  - All of them share the guard's policy, approvals and signed audit.
- **Negative:**
  - Two client codebases to keep in step (mitigated by the shared golden test and the real-binary test harness).
  - The proxy speaks stdio to clients only (no HTTP server mode yet).
  - Not yet published to PyPI/npm: installs from the repository.
  - Native adapters for the Claude Agent SDK and the OpenAI Agents SDK are not built. The MCP proxy covers MCP-based agents today.
