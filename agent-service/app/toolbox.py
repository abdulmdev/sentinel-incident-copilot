"""Agent tools, loaded from MCP servers.

- observability (Python MCP server): deploys, error logs, metrics
- knowledge (the gateway's MCP endpoint): tenant-scoped runbook search

The knowledge server is called with a downscoped, short-lived token that the gateway mints
per incident. It arrives in a request header and lives only in the run's config, never in
checkpointed state.
"""
from __future__ import annotations

import json
import os
import time
from collections import OrderedDict
from typing import Protocol

from langchain_core.tools import BaseTool
from langchain_mcp_adapters.client import MultiServerMCPClient


class ToolBox(Protocol):
    async def tools(self, tool_token: str | None) -> list[BaseTool]: ...

    async def remediation_tools(self, capability: str) -> list[BaseTool]: ...


class McpToolBox:
    """Discovers tools once per scoped token, not once per graph step.

    Listing tools costs an MCP handshake per server; tracing showed one incident making ~50 MCP
    requests for 6 tool calls because every node re-listed. The cache is keyed by the token (tools
    carry its auth header), so entries never cross tenants, and expires well inside the token's TTL.
    """

    CACHE_TTL_S = 300
    CACHE_SIZE = 256

    def __init__(self, observability_url: str | None = None, knowledge_url: str | None = None):
        self.observability_url = observability_url or os.getenv("SENTINEL_MCP_OBSERVABILITY_URL",
                                                                "http://localhost:8200/mcp")
        self.knowledge_url = knowledge_url or os.getenv("SENTINEL_MCP_KNOWLEDGE_URL", "http://localhost:8080/mcp")
        self._cache: OrderedDict[str | None, tuple[float, list[BaseTool]]] = OrderedDict()

    async def tools(self, tool_token: str | None) -> list[BaseTool]:
        hit = self._cache.get(tool_token)
        if hit and hit[0] > time.monotonic():
            self._cache.move_to_end(tool_token)
            return hit[1]
        tools = await self._discover(tool_token)
        self._cache[tool_token] = (time.monotonic() + self.CACHE_TTL_S, tools)
        while len(self._cache) > self.CACHE_SIZE:
            self._cache.popitem(last=False)
        return tools

    async def remediation_tools(self, capability: str) -> list[BaseTool]:
        """Write tools, authorized by the single-use capability minted at approval. Never cached."""
        url = os.getenv("SENTINEL_MCP_REMEDIATION_URL", "http://localhost:8300/mcp")
        return await MultiServerMCPClient({"remediation": {
            "transport": "streamable_http", "url": url,
            "headers": {"Authorization": f"Bearer {capability}"}}}).get_tools()

    async def _discover(self, tool_token: str | None) -> list[BaseTool]:
        # The tool token (aud sentinel-mcp, knowledge:read + telemetry:read) authenticates every MCP call.
        # Without one the observability server refuses (unless its auth is disabled for local runs).
        auth = {"headers": {"Authorization": f"Bearer {tool_token}"}} if tool_token else {}
        connections = {"observability": {"transport": "streamable_http", "url": self.observability_url, **auth}}
        if tool_token:
            connections["knowledge"] = {"transport": "streamable_http", "url": self.knowledge_url, **auth}
        return await MultiServerMCPClient(connections).get_tools()


def output_text(result) -> str:
    """Normalize a tool result (plain string, or MCP content blocks) to text."""
    if isinstance(result, tuple):  # (content, artifact)
        result = result[0]
    if isinstance(result, str):
        return result
    if isinstance(result, list):
        return "\n".join(b.get("text", "") if isinstance(b, dict) else str(b) for b in result)
    return json.dumps(result, default=str)
