# Example: guard any MCP server's tools, with no code

[`proxy.yaml`](proxy.yaml) puts Sentinel Guard in front of the reference filesystem MCP server. Reading passes through. Writing, editing, moving and creating directories each need a human's approval for exactly those arguments, and then run once.

1. Run the guard (single process):

   ```bash
   docker run --rm -p 8080:8080 -e SENTINEL_GUARD_AGENT_TOKENS=claude=agent-secret \
     -e SENTINEL_GUARD_ADMIN_TOKEN=approver-secret ghcr.io/abdulmdev/sentinel-guard:2.0.0-alpha.3
   ```

2. Install the SDK with the proxy:

   ```bash
   pip install "sentinel-guard[mcp] @ git+https://github.com/abdulmdev/sentinel-incident-copilot#subdirectory=sdk/python"
   ```

3. Point your MCP client at the proxy instead of the server. For Claude Code:

   ```bash
   claude mcp add files -e SENTINEL_GUARD_TOKEN=agent-secret -- sentinel-guard-mcp-proxy --config /path/to/proxy.yaml
   ```

   For Claude Desktop, Cursor and other clients, add the same command to their MCP server configuration:

   ```json
   {"mcpServers": {"files": {"command": "sentinel-guard-mcp-proxy", "args": ["--config", "/path/to/proxy.yaml"],
                             "env": {"SENTINEL_GUARD_TOKEN": "agent-secret"}}}}
   ```

When the agent tries to write a file, the tool answers "Not done yet: … needs human approval (request ar-…)". Approve it:

```bash
curl -X POST localhost:8080/v1/actions/<id>/approve -H 'Authorization: Bearer approver-secret' -d '{"approver":"you@example.com"}'
```

The agent's next identical call runs, once. In Kubernetes, approve with `kubectl sentinel approve <id>` instead.
