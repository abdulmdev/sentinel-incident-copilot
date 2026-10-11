from app import toolbox


async def test_tools_are_discovered_once_per_token(monkeypatch):
    calls = []

    async def discover(self, token):
        calls.append(token)
        return [f"tools-for-{token}"]
    monkeypatch.setattr(toolbox.McpToolBox, "_discover", discover)
    box = toolbox.McpToolBox()

    for _ in range(5):  # investigator + tools nodes across one incident
        assert await box.tools("tok-acme") == ["tools-for-tok-acme"]
    assert await box.tools("tok-globex") == ["tools-for-tok-globex"]
    assert calls == ["tok-acme", "tok-globex"]  # never shared across tokens (tenants)


async def test_cache_expires_and_is_bounded(monkeypatch):
    calls = []

    async def discover(self, token):
        calls.append(token)
        return []
    monkeypatch.setattr(toolbox.McpToolBox, "_discover", discover)
    box = toolbox.McpToolBox()
    box.CACHE_SIZE = 2
    for t in ("a", "b", "c"):
        await box.tools(t)
    assert list(box._cache) == ["b", "c"]  # LRU eviction
    box._cache["c"] = (0, [])               # expired
    await box.tools("c")
    assert calls == ["a", "b", "c", "c"]
