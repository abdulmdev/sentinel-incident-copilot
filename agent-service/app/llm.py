"""Model providers, routing, structured output, and cost accounting.

SENTINEL_LLM_PROVIDER selects the backend:
  anthropic (default)  Claude. Triage runs on a cheap fast model, investigation and RCA on the
                       most capable one. Active when ANTHROPIC_API_KEY is set.
  ollama               a local open-weight model (SENTINEL_OLLAMA_MODEL) for every step. Free,
                       private, offline; structured calls are grammar-constrained to the schema.
With no provider active, a scripted investigator and rule-based triage/RCA run instead
(heuristics.py); those are also the baseline the eval harness compares against.
"""
from __future__ import annotations

import json
import os
import re
import time
from typing import Type, TypeVar

from pydantic import BaseModel

TRIAGE_MODEL = os.getenv("SENTINEL_TRIAGE_MODEL", "claude-haiku-4-5")
INVESTIGATION_MODEL = os.getenv("SENTINEL_INVESTIGATION_MODEL", "claude-opus-5-5")
RCA_MODEL = os.getenv("SENTINEL_RCA_MODEL", "claude-opus-5-5")
_CLAUDE_BY_STEP = {"triage": TRIAGE_MODEL, "investigate": INVESTIGATION_MODEL, "rca": RCA_MODEL}

# USD per million tokens (input, output)
PRICES = {
    "claude-opus-5-5": (4.00, 20.00),
    "claude-sonnet-5-5": (2.00, 10.00),
    "claude-haiku-4-5": (1.00, 5.00),
}

T = TypeVar("T", bound=BaseModel)


def provider() -> str:
    return os.getenv("SENTINEL_LLM_PROVIDER", "anthropic").lower()


def llm_enabled() -> bool:
    if provider() == "ollama":
        return True
    return bool(os.getenv("ANTHROPIC_API_KEY"))


def model_for(step: str) -> str:
    """step: triage | investigate | rca"""
    if provider() == "ollama":
        return os.getenv("SENTINEL_OLLAMA_MODEL", "gemma4:latest")
    return _CLAUDE_BY_STEP[step]


def chat_model(model: str, schema: dict | None = None):
    """Factory seam: tests swap this for a scripted fake model.

    `schema`: when given, the provider constrains output to it where supported (Ollama `format`).
    """
    if provider() == "ollama":
        from langchain_ollama import ChatOllama
        reasoning = os.getenv("SENTINEL_OLLAMA_REASONING")  # unset = model default
        return ChatOllama(model=model, base_url=os.getenv("OLLAMA_BASE_URL", "http://localhost:11434"),
                          temperature=0, num_ctx=int(os.getenv("SENTINEL_OLLAMA_NUM_CTX", "16384")),
                          format=schema, keep_alive="30m",
                          reasoning=None if reasoning is None else reasoning.lower() == "true")
    from langchain_anthropic import ChatAnthropic
    return ChatAnthropic(model=model, max_tokens=16000, max_retries=2)


def cost_usd(model: str, usage: dict | None) -> float:
    if not usage:
        return 0.0
    if model not in PRICES and provider() == "ollama":
        return 0.0  # local inference: no per-token bill (tokens are still recorded)
    price_in, price_out = PRICES.get(model, PRICES["claude-opus-5-5"])  # unknown model: assume the dearest
    return (usage.get("input_tokens", 0) * price_in + usage.get("output_tokens", 0) * price_out) / 1_000_000


def text_of(message) -> str:
    # Responses can contain thinking blocks before the text; keep only the text blocks.
    content = message.content
    if isinstance(content, str):
        return content
    return "".join(b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text")


def _extract_json(text: str) -> str:
    fenced = re.search(r"```(?:json)?\s*(\{.*\})\s*```", text, re.S)
    if fenced:
        return fenced.group(1)
    start, end = text.find("{"), text.rfind("}")
    return text[start:end + 1] if start != -1 else text


async def invoke_structured(model: str, system: str, user: str, schema: Type[T],
                            json_schema: dict | None = None) -> tuple[T, dict]:
    """Ask for JSON that matches `schema`, then validate it with pydantic.

    The schema goes in the prompt instead of a forced tool call because the current
    Opus/Sonnet models reject a forced `tool_choice`.
    """
    json_schema = json_schema or schema.model_json_schema()
    prompt = (f"{user}\n\nRespond with only a JSON object matching this JSON Schema:\n"
              f"{json.dumps(json_schema)}")
    from .telemetry import record_llm_call, tracer
    started = time.perf_counter()
    with tracer.start_as_current_span(f"chat {model}") as span:
        msg = await chat_model(model, schema=json_schema).ainvoke([("system", system), ("human", prompt)])
        usage = getattr(msg, "usage_metadata", None) or {}
        record_llm_call(span, provider=provider(), model=model, usage=usage,
                        cost_usd=cost_usd(model, usage), step=schema.__name__)
    call = {"model": model, "latency_ms": round((time.perf_counter() - started) * 1000),
            "input_tokens": usage.get("input_tokens"), "output_tokens": usage.get("output_tokens"),
            "cost_usd": round(cost_usd(model, usage), 6)}
    return schema.model_validate_json(_extract_json(text_of(msg))), call
