"""An MCP proxy that puts Sentinel Guard in front of any MCP server's tools, with no agent changes.

    sentinel-guard-mcp-proxy --config proxy.yaml

    guard: {url: http://localhost:8080}          # token: SENTINEL_GUARD_TOKEN (or token_env / token_file)
    upstream:
      command: [npx, -y, "@modelcontextprotocol/server-filesystem", /srv/data]   # stdio, or:
      # url: http://localhost:9000/mcp                                         # streamable HTTP
    default: pass                                 # tools not listed: pass | guard | deny
    tools:
      write_file: {target: "{path}"}              # guarded (the default mode for a listed tool)
      move_file: {target: "{source}", action: move}
      delete_everything: {mode: deny}

Point the MCP client (Claude Desktop or Code, Cursor, an agent framework) at this command instead of
the server. Guarded calls are proposed to the guard with all their arguments. If one is allowed,
the capability is redeemed and the call is forwarded, once. Otherwise the client gets an error
result explaining why ("needs human approval (request ar-…)") and the upstream server is never
called. Unlisted tools pass through unchanged (or are guarded or denied, per `default`).
"""
from __future__ import annotations

import argparse
import asyncio
import contextlib
import os
from dataclasses import dataclass
from typing import Any

import mcp.types as types
import yaml
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.client.streamable_http import streamablehttp_client
from mcp.server.lowlevel import Server
from mcp.server.stdio import stdio_server

from .client import AsyncGuard, GuardError, GuardUnavailable, _resolve

MODES = ("pass", "guard", "deny")


@dataclass
class Rule:
    mode: str = "guard"
    action: str | None = None
    target: Any = None        # "{arg}" template; default: the tool's name
    namespace: Any = None
    change: Any = None        # list of argument names; default: all arguments


def load_rules(config: dict) -> tuple[dict[str, Rule], str]:
    default = config.get("default", "pass")
    if default not in MODES:
        raise ValueError(f"default must be one of {MODES}, got {default!r}")
    rules = {}
    for name, spec in (config.get("tools") or {}).items():
        spec = spec or {}
        unknown = set(spec) - {"mode", "action", "target", "namespace", "change"}
        if unknown:
            raise ValueError(f"tool {name}: unknown keys {sorted(unknown)}")
        rule = Rule(**spec)
        if rule.mode not in MODES:
            raise ValueError(f"tool {name}: mode must be one of {MODES}")
        rules[name] = rule
    return rules, default


def _refused(message: str) -> types.CallToolResult:
    return types.CallToolResult(content=[types.TextContent(type="text", text=message)], isError=True)


class GuardProxy:
    def __init__(self, upstream: ClientSession, guard: AsyncGuard, rules: dict[str, Rule], default: str = "pass",
                 name: str = "sentinel-guard-proxy"):
        self.upstream, self.guard, self.rules, self.default = upstream, guard, rules, default
        self.server = Server(name)
        self.server.list_tools()(self.list_tools)
        # The upstream server validates its own inputs; the proxy must not reject what it would accept.
        self.server.call_tool(validate_input=False)(self.call_tool)

    def rule(self, tool: str) -> Rule:
        return self.rules.get(tool) or Rule(mode=self.default)

    async def list_tools(self) -> list[types.Tool]:
        tools = []
        for t in (await self.upstream.list_tools()).tools:
            mode = self.rule(t.name).mode
            if mode == "deny":
                continue  # an agent shouldn't plan around a tool it may never use
            if mode == "guard":
                note = "(Guarded by Sentinel Guard: runs only after a human approves this exact call.)"
                t = t.model_copy(update={"description": f"{t.description or ''}\n{note}".strip()})
            tools.append(t)
        return tools

    async def call_tool(self, name: str, arguments: dict | None) -> types.CallToolResult:
        args = arguments or {}
        rule = self.rule(name)
        if rule.mode == "deny":
            return _refused(f"Sentinel Guard: the tool {name!r} is not available to agents.")
        if rule.mode == "guard":
            change = {k: args.get(k) for k in rule.change} if isinstance(rule.change, list) else args
            try:
                d = await self.guard.propose(rule.action or name, str(_resolve(rule.target, args, name)), change,
                                             intent=f"MCP tool call {name}", namespace=_resolve(rule.namespace, args))
                if d.verdict == "pending":
                    return _refused(f"Not done yet: {d.message} A human must approve it; then call the tool again "
                                    "with the same arguments.")
                if d.verdict != "allow" or not d.capability:
                    return _refused(f"Not done: {d.message}")
                await self.guard.spend(d.capability, executor=f"mcp-proxy:{name}")
            except GuardUnavailable:
                return _refused("Not done: Sentinel Guard is unavailable, so this tool can't run now.")
            except GuardError as e:
                return _refused(f"Not done: {e}")
        return await self.upstream.call_tool(name, args)


@contextlib.asynccontextmanager
async def connect_upstream(spec: dict):
    if "url" in spec:
        async with streamablehttp_client(spec["url"], headers=spec.get("headers")) as (read, write, _):
            async with ClientSession(read, write) as session:
                await session.initialize()
                yield session
        return
    command = spec.get("command")
    if not command:
        raise ValueError("upstream needs `command` (stdio) or `url` (streamable HTTP)")
    params = StdioServerParameters(command=command[0], args=list(command[1:]), env={**os.environ, **(spec.get("env") or {})},
                                   cwd=spec.get("cwd"))
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            yield session


def guard_from_config(spec: dict) -> AsyncGuard:
    token = spec.get("token")
    if token is None and spec.get("token_env"):
        token = os.getenv(spec["token_env"])
    return AsyncGuard(spec.get("url"), token=token, token_file=spec.get("token_file"))


async def serve(config: dict) -> None:
    rules, default = load_rules(config)
    guard = guard_from_config(config.get("guard") or {})
    async with connect_upstream(config.get("upstream") or {}) as upstream:
        proxy = GuardProxy(upstream, guard, rules, default)
        async with stdio_server() as (read, write):
            await proxy.server.run(read, write, proxy.server.create_initialization_options())


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description="Put Sentinel Guard in front of any MCP server's tools.")
    parser.add_argument("--config", required=True, help="proxy YAML (guard, upstream, default, tools)")
    opts = parser.parse_args(argv)
    with open(opts.config) as f:
        config = yaml.safe_load(f) or {}
    asyncio.run(serve(config))


if __name__ == "__main__":
    main()
