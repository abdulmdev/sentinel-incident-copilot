"""Guard any LangChain tool (and so any LangGraph agent's tools) with Sentinel Guard.

    from sentinel_guard import AsyncGuard
    from sentinel_guard.langchain import guard_tool

    guard = AsyncGuard()                                  # SENTINEL_GUARD_URL / _TOKEN
    tools = [search, guard_tool(delete_bucket, guard, target="{bucket}")]

The wrapped tool has the same name and arguments. Each call is proposed to the guard. If it is
allowed, the capability is redeemed and the real tool runs, once. Otherwise the model gets a plain
explanation instead of a result ("needs human approval (request ar-…)", or why it was blocked),
so it can tell the user and try the same call again after approval. Nothing runs on any doubt.
"""
from __future__ import annotations

import asyncio
from typing import Any

from langchain_core.tools import BaseTool, StructuredTool

from .client import AsyncGuard, Guard, GuardError, GuardUnavailable, _resolve

GUARDED_NOTE = "(Guarded by Sentinel Guard: runs only after policy or a human approves this exact call.)"


def guard_tool(tool: BaseTool, guard: AsyncGuard | Guard, action: str | None = None, target: Any = None,
               change: Any = None, namespace: Any = None, intent: Any = None, executor: str | None = None) -> BaseTool:
    """Returns a guarded copy of `tool`.

    `action` defaults to the tool's name; `target`, `change`, `namespace` and `intent` are values,
    "{arg}" templates or callables on the call's arguments. By default the approval is bound to all
    arguments and the target is the tool's name.
    """
    name = action or tool.name

    async def propose(args: dict):
        t = str(_resolve(target, args, tool.name))
        ch, ns, why = _resolve(change, args, args), _resolve(namespace, args), _resolve(intent, args, "")
        if isinstance(guard, AsyncGuard):
            return await guard.propose(name, t, ch, why, ns)
        return await asyncio.to_thread(guard.propose, name, t, ch, why, ns)

    async def spend(token: str):
        if isinstance(guard, AsyncGuard):
            return await guard.spend(token, executor or tool.name)
        return await asyncio.to_thread(guard.spend, token, executor or tool.name)

    async def run(**args):
        try:
            d = await propose(args)
            if d.verdict == "pending":
                return (f"Not done yet: {d.message} Tell the user that a human must approve it, then call this tool "
                        "again with the same arguments.")
            if d.verdict != "allow" or not d.capability:
                return f"Not done: {d.message}"
            await spend(d.capability)
        except GuardUnavailable:
            return "Not done: Sentinel Guard is unavailable, so this action can't run now."
        except GuardError as e:
            return f"Not done: {e}"
        return await tool.ainvoke(args)

    return StructuredTool.from_function(coroutine=run, name=tool.name,
                                        description=f"{tool.description}\n{GUARDED_NOTE}".strip(),
                                        args_schema=tool.args_schema, infer_schema=False)
