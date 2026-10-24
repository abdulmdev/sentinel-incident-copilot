# sentinel-guard (Python SDK)

Ask [Sentinel Guard](../../docs/guard/README.md) before your agent acts. The guard answers in one of three ways:
- **allowed now**, by your policy;
- **wait for a human**, with a request id to show them;
- **blocked**.

Once approved you get a **single-use capability** for exactly that action and its parameters. Redeem it right before the side effect: once, ever.

```bash
pip install "sentinel-guard @ git+https://github.com/abdulmdev/sentinel-incident-copilot#subdirectory=sdk/python"
```

Add `[langchain]` for the LangChain/LangGraph tool wrapper, `[mcp]` for the MCP proxy.

## Three lines around any action

```python
from sentinel_guard import Guard, ApprovalPending

guard = Guard("http://localhost:8080", token="agent-secret")   # or SENTINEL_GUARD_URL / _TOKEN / _TOKEN_FILE

try:
    cap = guard.require("refund", target="order-1042", change={"amount": "42.00 EUR"},
                        intent="customer was charged twice", wait=300)   # waits up to 5 min for a human
except ApprovalPending as p:
    print(p.message)        # "Sentinel Guard: refund of order-1042 needs human approval (request ar-…)."
    raise

guard.spend(cap.token, executor="payments")   # redeem once, immediately before acting
issue_refund("order-1042", "42.00 EUR")
```

`require` raises:
- `ApprovalPending` when a human must decide;
- `ActionBlocked` for a policy limit or a human denial;
- `GuardUnavailable` when the guard can't be reached.

`spend` raises `CapabilityRejected` if the capability was already used, has expired or is forged. **Any exception means: don't act.**

## Or as a decorator

```python
@guard.guarded("refund", target="{order_id}")        # approval is bound to every argument by default
def refund(order_id: str, amount: str): ...
```

`AsyncGuard` has the same API for `asyncio`.

## LangChain / LangGraph

```python
from sentinel_guard.langchain import guard_tool

tools = [guard_tool(delete_bucket, guard, target="{bucket}")]   # a guarded copy of any tool
```

While approval is pending, the wrapped tool returns a message to the model instead of acting (for example "needs human approval (request ar-…); tell the user"). Once approved, the same call runs, once.

## Any MCP server, no code: the proxy

```bash
sentinel-guard-mcp-proxy --config proxy.yaml
```

```yaml
guard: {url: http://localhost:8080}             # token from SENTINEL_GUARD_TOKEN
upstream: {command: [npx, -y, "@modelcontextprotocol/server-filesystem", /srv/data]}
default: pass                                    # tools not listed: pass | guard | deny
tools:
  write_file: {target: "{path}"}                 # guarded: needs approval, bound to its arguments
  move_file: {target: "{source}"}
```

Point your MCP client (Claude Desktop or Code, Cursor, an agent framework) at the proxy instead of the server. Listed tools run only once approved; everything else passes through.

## Where to redeem
- **In-process** (the decorator, `guard_tool`): the guard decides and records every action. This protects against the model's mistakes and manipulation, not against a compromised process.
- **In a separate executor** (`spend` in the service that performs the action), **the MCP proxy**, or **the Kubernetes webhook**: the agent holds no power to act on its own.
