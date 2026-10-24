"""The proxy in front of a real (in-memory) MCP server, with the real guard deciding."""
import contextlib
import re
import sys
import textwrap

import pytest
from mcp import StdioServerParameters
from mcp.server.fastmcp import FastMCP
from mcp.shared.memory import create_connected_server_and_client_session

from sentinel_guard import AsyncGuard
from sentinel_guard.mcp_proxy import GuardProxy, Rule, load_rules

from .conftest import AGENT_TOKEN

calls: list[tuple[str, dict]] = []
upstream = FastMCP("files")


@upstream.tool()
def read_file(path: str) -> str:
    """Read a file."""
    calls.append(("read_file", {"path": path}))
    return f"contents of {path}"


@upstream.tool()
def delete_file(path: str, recursive: bool = False) -> str:
    """Delete a file."""
    calls.append(("delete_file", {"path": path, "recursive": recursive}))
    return f"deleted {path}"


@upstream.tool()
def format_disk(device: str) -> str:
    """Erase a disk."""
    calls.append(("format_disk", {"device": device}))
    return "formatted"


def text(result) -> str:
    return result.content[0].text


@contextlib.asynccontextmanager
async def proxied(guard_url, rules=None, default="pass"):
    """An MCP client → the proxy → the upstream server, all in this task (anyio needs that)."""
    calls.clear()
    if rules is None:
        rules = {"delete_file": Rule(target="{path}"), "format_disk": Rule(mode="deny")}
    async with create_connected_server_and_client_session(upstream._mcp_server) as up:
        proxy = GuardProxy(up, AsyncGuard(guard_url, token=AGENT_TOKEN), rules, default)
        async with create_connected_server_and_client_session(proxy.server) as client:
            yield client


async def test_lists_upstream_tools_marks_guarded_and_hides_denied(guard_url):
    async with proxied(guard_url) as client:
        tools = {t.name: t for t in (await client.list_tools()).tools}
        assert set(tools) == {"read_file", "delete_file"}
        assert "Sentinel Guard" in tools["delete_file"].description
        assert "Sentinel Guard" not in tools["read_file"].description
        assert tools["delete_file"].inputSchema["properties"]["path"]  # the upstream schema, unchanged


async def test_unguarded_tools_pass_through(guard_url):
    async with proxied(guard_url) as client:
        assert text(await client.call_tool("read_file", {"path": "/srv/a"})) == "contents of /srv/a"


async def test_guarded_tool_runs_only_after_approval_and_once(guard_url, approve):
    async with proxied(guard_url) as client:
        first = await client.call_tool("delete_file", {"path": "/srv/a"})
        assert first.isError and "needs human approval" in text(first)
        assert [c for c in calls if c[0] == "delete_file"] == []
        approve(re.search(r"ar-[0-9a-f]{8}-\d+", text(first)).group(0))

        done = await client.call_tool("delete_file", {"path": "/srv/a"})
        assert not done.isError and text(done) == "deleted /srv/a"
        again = await client.call_tool("delete_file", {"path": "/srv/a"})
        assert again.isError and "needs human approval" in text(again)
        other = await client.call_tool("delete_file", {"path": "/srv/a", "recursive": True})
        assert other.isError  # different arguments: not covered by the approval
        assert [c for c in calls if c[0] == "delete_file"] == [("delete_file", {"path": "/srv/a", "recursive": False})]


async def test_denied_tools_never_reach_the_server(guard_url):
    async with proxied(guard_url) as client:
        result = await client.call_tool("format_disk", {"device": "/dev/sda"})
        assert result.isError and "not available to agents" in text(result)
        assert not [c for c in calls if c[0] == "format_disk"]


async def test_guard_down_fails_closed():
    async with proxied("http://127.0.0.1:9", {"delete_file": Rule()}) as client:
        result = await client.call_tool("delete_file", {"path": "/srv/a"})
        assert result.isError and "unavailable" in text(result)
    assert not [c for c in calls if c[0] == "delete_file"]


async def test_default_guard_covers_unlisted_tools(guard_url):
    async with proxied(guard_url, {}, default="guard") as client:
        assert (await client.call_tool("read_file", {"path": "/x"})).isError
    assert calls == []


def test_config_validation():
    rules, default = load_rules({"default": "deny", "tools": {"write_file": {"target": "{path}"}, "rm": None}})
    assert default == "deny" and rules["write_file"].mode == "guard" and rules["rm"].mode == "guard"
    for bad in ({"default": "maybe"}, {"tools": {"x": {"mode": "sometimes"}}}, {"tools": {"x": {"taget": "{p}"}}}):
        with pytest.raises(ValueError):
            load_rules(bad)


async def test_the_cli_over_stdio(guard_url, approve, tmp_path):
    """The real entry point: an MCP client launches the proxy, which launches the upstream server."""
    from mcp import ClientSession
    from mcp.client.stdio import stdio_client

    server_py = tmp_path / "upstream.py"
    server_py.write_text(textwrap.dedent('''
        from mcp.server.fastmcp import FastMCP
        mcp = FastMCP("up")
        @mcp.tool()
        def drop_table(table: str) -> str:
            """Drop a table."""
            return f"dropped {table}"
        mcp.run()
    '''))
    config = tmp_path / "proxy.yaml"
    config.write_text(textwrap.dedent(f'''
        guard: {{url: "{guard_url}", token_env: PROXY_GUARD_TOKEN}}
        upstream: {{command: ["{sys.executable}", "{server_py}"]}}
        tools:
          drop_table: {{target: "{{table}}"}}
    '''))
    params = StdioServerParameters(command=sys.executable, args=["-m", "sentinel_guard.mcp_proxy", "--config", str(config)],
                                   env={"PROXY_GUARD_TOKEN": AGENT_TOKEN, "PATH": "/usr/bin:/bin"})
    async with stdio_client(params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            first = await session.call_tool("drop_table", {"table": "orders"})
            assert first.isError
            approve(re.search(r"ar-[0-9a-f]{8}-\d+", first.content[0].text).group(0))
            done = await session.call_tool("drop_table", {"table": "orders"})
            assert not done.isError and done.content[0].text == "dropped orders"
