"""Test doubles: MCP-shaped tools and a scripted chat model."""
from __future__ import annotations

import itertools
import json
from typing import Any, Callable

from langchain_core.language_models import BaseChatModel
from langchain_core.messages import AIMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langchain_core.tools import StructuredTool

BAD_DEPLOY = {
    "deploys": [{"version": "1.5.0", "commit": "a1b2c3d", "ts": "2026-10-05T10:00:00Z",
                 "change": "switch payment client to async pool"}],
    "ERROR": [{"message": "NullPointerException in PaymentClient.charge", "count": 41,
               "latest": {"ts": "t", "version": "1.5.0", "stack": "PaymentClient.charge:88"}}],
    "WARN": [],
    "metrics": {'chaos_requests_total{status="error"}': 41.0, "chaos_db_pool_in_use": 4.0},
    "runbooks": [{"docId": "checkout-rollback", "title": "Checkout rollback runbook",
                  "heading": "Checkout rollback runbook > Decide", "text": "Roll back first, debug later."},
                 {"docId": "postmortem-2026-08-14-checkout-npe", "title": "Postmortem",
                  "heading": "Postmortem > Summary", "text": "NullPointerException in PaymentClient.charge after 1.3.0"}],
}


class FakeToolBox:
    """Same tool names and output shapes as the real MCP servers (JSON text)."""

    def __init__(self, world: dict = BAD_DEPLOY, fail: set[str] = frozenset(), heals: bool = True,
                 refuse_capability: bool = False):
        self.world, self.fail = world, fail
        self.calls: list[tuple[str, dict]] = []
        self.tokens: list[str | None] = []
        self.capabilities: list[str] = []
        self.heals, self.refuse_capability = heals, refuse_capability
        self.healthy = False  # becomes True once an executed remediation "fixes" the service

    async def remediation_tools(self, capability):
        """Same contract as mcp-tools/remediation: one tool per action, JSON result, errors as text."""
        self.capabilities.append(capability)

        def make(action):
            async def run(**kwargs):
                self.calls.append((action, kwargs))
                if self.refuse_capability:
                    return "Error executing tool: capability already used"
                self.healthy = self.heals
                return json.dumps({"executed": True, "dry_run": False, "action": action,
                                   "service": kwargs["service"], "result": {"fixed_active_fault": self.heals}})
            props = {"service": {"type": "string"}, "size": {"type": "integer"}, "dry_run": {"type": "boolean"}}
            return StructuredTool.from_function(coroutine=run, name=action, description=action,
                                                args_schema={"type": "object", "properties": props,
                                                             "required": ["service"]})
        return [make(a) for a in ("rollback", "restart", "increase_pool", "failover", "scale_out")]

    async def tools(self, tool_token):
        self.tokens.append(tool_token)

        def make(name: str, fn: Callable[..., Any], desc: str, props: dict, required: list[str]):
            async def run(**kwargs):
                self.calls.append((name, kwargs))
                if name in self.fail:
                    raise RuntimeError(f"{name} backend down")
                return json.dumps(fn(**kwargs))
            # JSON-schema args, like the tools langchain-mcp-adapters builds from MCP tool definitions
            schema = {"type": "object", "properties": props, "required": required}
            return StructuredTool.from_function(coroutine=run, name=name, description=desc, args_schema=schema)

        svc = {"service": {"type": "string"}}

        w = self.world
        tools = [
            # Same wire shapes as mcp-tools/observability (one JSON object per result)
            make("get_recent_deploys", lambda service: {"service": service, "deploys": w["deploys"]},
                 "deploys", svc, ["service"]),
            make("get_error_logs", lambda service, level="ERROR": {"service": service, "level": level,
                                                                   "logs": w[level.upper()]},
                 "logs", {**svc, "level": {"type": "string"}}, ["service"]),
            make("get_service_metrics", lambda service: {"service": service, "metrics": w["metrics"]},
                 "metrics", svc, ["service"]),
        ]
        tools.append(make("probe_service",
                          lambda service, requests=20: {"service": service, "requests": requests,
                                                        "success_rate": 1.0 if self.healthy else 0.6,
                                                        "p95_ms": 40.0 if self.healthy else 2500.0},
                          "synthetic probe", {**svc, "requests": {"type": "integer"}}, ["service"]))
        if tool_token:  # mirrors McpToolBox: knowledge tools need the scoped token
            tools.append(make("search_runbooks", lambda query, limit=4: w["runbooks"][:limit], "runbooks",
                              {"query": {"type": "string"}, "limit": {"type": "integer"}}, ["query"]))
        return tools


class ScriptedChatModel(BaseChatModel):
    """Returns `responder(messages)`; ignores tool binding. Lets tests drive the LLM loop."""

    responder: Any
    counter: Any = None

    def bind_tools(self, tools, **kwargs):
        return self

    def _generate(self, messages, stop=None, run_manager=None, **kwargs):
        return ChatResult(generations=[ChatGeneration(message=self.responder(messages))])

    @property
    def _llm_type(self) -> str:
        return "scripted"


_ids = itertools.count()


def tool_calls_reply(*calls: tuple[str, dict], usage: dict | None = None) -> AIMessage:
    return AIMessage(content="", tool_calls=[{"name": n, "args": a, "id": f"tc_{next(_ids)}", "type": "tool_call"}
                                             for n, a in calls],
                     usage_metadata=usage or {"input_tokens": 1000, "output_tokens": 100, "total_tokens": 1100})
