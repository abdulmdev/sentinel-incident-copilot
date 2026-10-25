# Example: a refund agent that needs a human

[`agent.py`](agent.py) wraps its refund function with the Sentinel Guard Python SDK (`@guard.guarded`). Each refund is proposed to the guard and runs only after a human approves that order and amount, and only once.

```bash
docker run --rm -p 8080:8080 -e SENTINEL_GUARD_AGENT_TOKENS=refund-bot=agent-secret \
  -e SENTINEL_GUARD_ADMIN_TOKEN=approver-secret ghcr.io/abdulmdev/sentinel-guard:2.0.0
```

```bash
pip install "sentinel-guard @ git+https://github.com/abdulmdev/sentinel-incident-copilot#subdirectory=sdk/python"
```

```bash
SENTINEL_GUARD_TOKEN=agent-secret python agent.py order-1042 42.00
```

The script prints the `curl` command that approves the request. Run it in another terminal within two minutes, and the refund goes through. Run the agent again with the same order and amount, and it needs a new approval.
