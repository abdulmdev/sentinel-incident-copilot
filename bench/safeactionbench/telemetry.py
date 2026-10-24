"""The telemetry an agent can query for an incident, replayed from the incident's recorded world.

Shapes match Sentinel's MCP observability tools (deploys, grouped logs, metrics) and its runbook
search, so agents built for real MCP telemetry work unchanged. A tool listed in the incident's
`tool_failures` raises, as a real backend outage would.
"""
from __future__ import annotations

import math
import re
from collections import Counter
from functools import lru_cache
from importlib import resources

TOKEN = re.compile(r"[a-z0-9]+")
STOP = {"the", "a", "an", "and", "or", "of", "to", "in", "on", "for", "is", "it", "be", "with", "this", "that",
        "from", "at", "by", "as", "are", "if", "when", "what", "do", "i", "we", "not"}


class ToolUnavailable(RuntimeError):
    pass


def _tokens(text: str) -> list[str]:
    return [t for t in TOKEN.findall(text.lower()) if t not in STOP]


class RunbookIndex:
    """BM25 over heading-split runbook sections (the team's runbooks and postmortems)."""

    def __init__(self):
        self.chunks = []
        root = resources.files("safeactionbench").joinpath("data", "runbooks")
        for path in sorted((p for p in root.iterdir() if p.name.endswith(".md")), key=lambda p: p.name):
            text = path.read_text()
            doc = path.name.removesuffix(".md")
            title = next((l[2:].strip() for l in text.splitlines() if l.startswith("# ")), doc)
            for section in re.split(r"\n(?=## )", text):
                heading = section.splitlines()[0].lstrip("# ").strip()
                body = "\n".join(section.splitlines()[1:]).strip()
                if body:
                    self.chunks.append({"docId": doc, "title": title, "heading": f"{title} > {heading}", "text": body,
                                        "tokens": _tokens(f"{heading} {body}")})
        self.avg_len = sum(len(c["tokens"]) for c in self.chunks) / len(self.chunks)
        df = Counter(t for c in self.chunks for t in set(c["tokens"]))
        n = len(self.chunks)
        self.idf = {t: math.log(1 + (n - f + 0.5) / (f + 0.5)) for t, f in df.items()}

    def search(self, query: str, limit: int = 4) -> list[dict]:
        q = set(_tokens(query))
        scored = []
        for c in self.chunks:
            tf = Counter(c["tokens"])
            s = sum(self.idf.get(t, 0) * tf[t] * 2.2 / (tf[t] + 1.2 * (0.25 + 0.75 * len(c["tokens"]) / self.avg_len))
                    for t in q if t in tf)
            if s > 0:
                scored.append((s, c))
        scored.sort(key=lambda x: -x[0])
        return [{k: c[k] for k in ("docId", "title", "heading", "text")} for _, c in scored[:max(1, min(limit, 8))]]


@lru_cache(maxsize=1)
def runbooks() -> RunbookIndex:
    return RunbookIndex()


class Telemetry:
    """The tools for one incident."""

    def __init__(self, scenario: dict):
        self.world = scenario["world"]
        self.failures = set(scenario.get("tool_failures", []))

    def _check(self, tool: str) -> None:
        if tool in self.failures:
            raise ToolUnavailable("telemetry backend unavailable (503)")

    def _svc(self, service: str) -> dict:
        if service not in self.world:
            raise ValueError(f"unknown service {service!r}; known: {sorted(self.world)}")
        return self.world[service]

    def list_services(self) -> dict:
        self._check("list_services")
        return {"services": sorted(self.world)}

    def get_recent_deploys(self, service: str) -> dict:
        self._check("get_recent_deploys")
        return {"service": service, "deploys": self._svc(service)["deploys"]}

    def get_error_logs(self, service: str, level: str = "ERROR") -> dict:
        self._check("get_error_logs")
        level = level.upper()
        if level not in ("ERROR", "WARN"):
            raise ValueError("level must be ERROR or WARN")
        return {"service": service, "level": level, "logs": self._svc(service)[level]}

    def get_service_metrics(self, service: str) -> dict:
        self._check("get_service_metrics")
        return {"service": service, "metrics": self._svc(service)["metrics"]}

    def search_runbooks(self, query: str, limit: int = 4) -> list[dict]:
        self._check("search_runbooks")
        return runbooks().search(query, limit)
