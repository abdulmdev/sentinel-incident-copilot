import re
import uuid

from langchain_core.tools import tool

from sentinel_guard import AsyncGuard, Guard
from sentinel_guard.langchain import guard_tool

from .conftest import AGENT_TOKEN

deleted: list[str] = []


@tool
def delete_bucket(bucket: str, force: bool = False) -> str:
    """Delete a storage bucket."""
    deleted.append(bucket)
    return f"deleted {bucket}"


async def test_guarded_tool_waits_for_approval_then_runs_once(guard_url, approve):
    deleted.clear()
    bucket = f"logs-{uuid.uuid4().hex[:6]}"
    guarded = guard_tool(delete_bucket, AsyncGuard(guard_url, token=AGENT_TOKEN), target="{bucket}")
    assert guarded.name == "delete_bucket" and "Sentinel Guard" in guarded.description
    assert set(guarded.args) == {"bucket", "force"}

    first = await guarded.ainvoke({"bucket": bucket})
    assert first.startswith("Not done yet:") and "needs human approval" in first and deleted == []
    approve(re.search(r"ar-[0-9a-f]{8}-\d+", first).group(0))

    assert await guarded.ainvoke({"bucket": bucket}) == f"deleted {bucket}"
    assert deleted == [bucket]
    again = await guarded.ainvoke({"bucket": bucket})  # one approval, one execution
    assert again.startswith("Not done yet:") and deleted == [bucket]


async def test_sync_guard_and_an_unreachable_guard(guard_url):
    sync = guard_tool(delete_bucket, Guard(guard_url, token=AGENT_TOKEN), target="{bucket}")
    assert (await sync.ainvoke({"bucket": "b1"})).startswith("Not done yet:")
    down = guard_tool(delete_bucket, AsyncGuard("http://127.0.0.1:9", token=AGENT_TOKEN, timeout=0.5))
    assert "unavailable" in await down.ainvoke({"bucket": "b2"})
