from app import llm


def test_ollama_provider_is_local_free_and_needs_no_key(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.setenv("SENTINEL_LLM_PROVIDER", "ollama")
    monkeypatch.setenv("SENTINEL_OLLAMA_MODEL", "gemma4:latest")
    assert llm.llm_enabled()
    assert {llm.model_for(s) for s in ("triage", "investigate", "rca")} == {"gemma4:latest"}
    assert llm.cost_usd("gemma4:latest", {"input_tokens": 10_000, "output_tokens": 5_000}) == 0.0
    model = llm.chat_model("gemma4:latest", schema={"type": "object"})
    assert type(model).__name__ == "ChatOllama" and model.format == {"type": "object"} and model.temperature == 0


def test_anthropic_provider_routes_cheap_and_strong_models(monkeypatch):
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    assert not llm.llm_enabled()  # no key, no Claude: the scripted baseline runs
    assert llm.model_for("triage") == "claude-haiku-4-5"
    assert llm.model_for("rca") == "claude-opus-5-5"
    assert llm.cost_usd("claude-opus-5-5", {"input_tokens": 1_000_000, "output_tokens": 0}) == 4.0
