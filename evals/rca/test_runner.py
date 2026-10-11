"""The LLM benchmark must never record a rules fallback under the model's name."""
import asyncio
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).parent))

import run  # noqa: E402
from replay import RunbookIndex  # noqa: E402

SCENARIO = json.loads((pathlib.Path(__file__).parent / "scenarios.jsonl").read_text().splitlines()[0])


def one(require_llm: bool, monkeypatch) -> dict:
    # No provider configured: the agent investigates with rules, exactly as when a model is unreachable.
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
    return asyncio.run(run.run_one(SCENARIO, RunbookIndex(), asyncio.Semaphore(1), require_llm=require_llm))


def test_rules_run_is_a_valid_baseline_result(monkeypatch):
    r = one(False, monkeypatch)
    assert r["error"] is None and "llm_unavailable" not in r


def test_llm_run_without_any_model_call_is_flagged_not_scored(monkeypatch):
    r = one(True, monkeypatch)
    assert r["llm_unavailable"] is True
    assert "LLM unavailable" in r["error"]
