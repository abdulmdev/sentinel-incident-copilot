"""Replays a scenario's recorded telemetry through MCP-shaped tools. No network, fully deterministic.

Output shapes match mcp-tools/observability and the gateway's search_runbooks exactly
(see contracts/README.md), so the agent under test runs its real parsing and loop code.
"""
from __future__ import annotations

import json
import math
import pathlib
import re
from collections import Counter

from langchain_core.tools import StructuredTool

KNOWLEDGE = pathlib.Path(__file__).resolve().parents[2] / "knowledge" / "acme"
TOKEN = re.compile(r"[a-z0-9]+")
STOP = {"the", "a", "an", "and", "or", "of", "to", "in", "on", "for", "is", "it", "be", "with", "this", "that",
        "from", "at", "by", "as", "are", "if", "when", "what", "do", "i", "we", "not"}


def _tokens(text: str) -> list[str]:
    return [t for t in TOKEN.findall(text.lower()) if t not in STOP]


class RunbookIndex:
    """Offline stand-in for the gateway's hybrid search: BM25 over heading-split runbook sections.

    The live system uses pgvector + full-text with RRF (measured separately by evals/retrieval).
    This keeps the RCA benchmark hermetic: no Docker, no embedding model.
    """

    def __init__(self, root: pathlib.Path = KNOWLEDGE):
        self.chunks = []
        for path in sorted(root.glob("*.md")):
            text = path.read_text()
            title = next((l[2:].strip() for l in text.splitlines() if l.startswith("# ")), path.stem)
            for section in re.split(r"\n(?=## )", text):
                heading = section.splitlines()[0].lstrip("# ").strip()
                body = "\n".join(section.splitlines()[1:]).strip()
                if body:
                    self.chunks.append({"docId": path.stem, "title": title, "heading": f"{title} > {heading}",
                                        "text": body, "tokens": _tokens(f"{heading} {body}")})
        self.avg_len = sum(len(c["tokens"]) for c in self.chunks) / len(self.chunks)
        df = Counter(t for c in self.chunks for t in set(c["tokens"]))
        n = len(self.chunks)
        self.idf = {t: math.log(1 + (n - f + 0.5) / (f + 0.5)) for t, f in df.items()}

    def search(self, query: str, limit: int = 4) -> list[dict]:
        q = set(_tokens(query))
        scored = []
        for c in self.chunks:
            tf = Counter(c["tokens"])
            score = sum(self.idf.get(t, 0) * tf[t] * 2.2 / (tf[t] + 1.2 * (0.25 + 0.75 * len(c["tokens"]) / self.avg_len))
                        for t in q if t in tf)
            if score > 0:
                scored.append((score, c))
        scored.sort(key=lambda x: -x[0])
        return [{k: c[k] for k in ("docId", "title", "heading", "text")} for _, c in scored[:limit]]


class ReplayToolBox:
    def __init__(self, scenario: dict, index: RunbookIndex):
        self.world = scenario["world"]
        self.failures = set(scenario.get("tool_failures", []))
        self.index = index
        self.calls: list[tuple[str, dict]] = []

    def _svc(self, service: str) -> dict:
        if service not in self.world:
            raise ValueError(f"unknown service {service!r}; known: {sorted(self.world)}")
        return self.world[service]

    async def tools(self, tool_token):
        def make(name, fn, desc, props, required):
            async def run(**kwargs):
                self.calls.append((name, kwargs))
                if name in self.failures:
                    raise RuntimeError("telemetry backend unavailable (503)")
                return json.dumps(fn(**kwargs))
            schema = {"type": "object", "properties": props, "required": required}
            return StructuredTool.from_function(coroutine=run, name=name, description=desc, args_schema=schema)

        svc = {"service": {"type": "string", "description": "service name"}}
        tools = [
            make("list_services", lambda: {"services": sorted(self.world)}, "List services with telemetry.", {}, []),
            make("get_recent_deploys", lambda service: {"service": service, "deploys": self._svc(service)["deploys"]},
                 "Recent deployments of a service, oldest first.", svc, ["service"]),
            make("get_error_logs",
                 lambda service, level="ERROR": {"service": service, "level": level.upper(),
                                                 "logs": self._svc(service)[level.upper()]},
                 "Recent ERROR or WARN log lines of a service, grouped by message with counts.",
                 {**svc, "level": {"type": "string", "enum": ["ERROR", "WARN"]}}, ["service"]),
            make("get_service_metrics", lambda service: {"service": service, "metrics": self._svc(service)["metrics"]},
                 "Key metrics: request counts by status, latency, heap MB, DB pool in use (max 20).", svc, ["service"]),
        ]
        if tool_token:
            tools.append(make("search_runbooks", lambda query, limit=4: self.index.search(query, limit),
                              "Search runbooks and postmortems. Cite results by docId.",
                              {"query": {"type": "string"}, "limit": {"type": "integer"}}, ["query"]))
        return tools
