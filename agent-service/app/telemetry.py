"""OpenTelemetry tracing for the agent.

One trace per incident, continued from the gateway's `traceparent` header, with a span per
graph node and per tool call, and GenAI semantic-convention attributes on every model call
(model, token usage, cost). Outbound httpx calls (MCP servers) carry the trace onward.

Enabled when OTEL_EXPORTER_OTLP_ENDPOINT is set (or an exporter is passed, as tests do).
"""
from __future__ import annotations

import os

from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor, SimpleSpanProcessor

tracer = trace.get_tracer("sentinel.agent")
_configured = False


def setup(app=None, exporter=None, service_name: str = "sentinel-agent") -> TracerProvider | None:
    global _configured
    if _configured or (exporter is None and not os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")):
        return None
    from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor

    provider = TracerProvider(resource=Resource.create({"service.name": service_name}))
    if exporter is None:
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    else:
        provider.add_span_processor(SimpleSpanProcessor(exporter))
    trace.set_tracer_provider(provider)
    if app is not None:
        # exclude_spans: no span per ASGI message chunk; one server span per request is the useful unit
        FastAPIInstrumentor.instrument_app(app, tracer_provider=provider, excluded_urls="healthz,metrics",
                                           exclude_spans=["receive", "send"])
    HTTPXClientInstrumentor().instrument(tracer_provider=provider)
    _configured = True
    return provider


def record_llm_call(span, *, provider: str, model: str, usage: dict | None, cost_usd: float, step: str) -> None:
    """GenAI semantic conventions (gen_ai.*) plus our cost attribution."""
    usage = usage or {}
    span.set_attribute("gen_ai.operation.name", "chat")
    span.set_attribute("gen_ai.system", provider)
    span.set_attribute("gen_ai.request.model", model)
    if usage.get("input_tokens") is not None:
        span.set_attribute("gen_ai.usage.input_tokens", usage["input_tokens"])
    if usage.get("output_tokens") is not None:
        span.set_attribute("gen_ai.usage.output_tokens", usage["output_tokens"])
    span.set_attribute("sentinel.llm.step", step)
    span.set_attribute("sentinel.llm.cost_usd", cost_usd)
