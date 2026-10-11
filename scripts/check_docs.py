"""Fails when documentation drifts from code (run in CI).

  python3 scripts/check_docs.py

Checks:
  1. every relative Markdown link in docs/ and README.md resolves to a file
  2. every Flyway migration is documented in the data-model chapter
  3. every environment variable read by code is in the configuration reference
  4. every Prometheus metric we define is in the observability metrics catalog
  5. every diagram source (docs/handbook/diagrams/*.mmd) has an up-to-date rendered SVG + PNG
"""
from __future__ import annotations

import os
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
HANDBOOK = ROOT / "docs" / "handbook"
SKIP_DIRS = {"node_modules", ".venv", "target", ".next", ".tools", ".terraform", ".logs", ".models"}


def files(pattern: str, under: pathlib.Path = ROOT):
    for p in under.rglob(pattern):
        if not SKIP_DIRS.intersection(p.parts):
            yield p


def check_links() -> list[str]:
    errors = []
    docs = [ROOT / "README.md", *files("*.md", ROOT / "docs"), *files("README.md", ROOT / "evals"),
            ROOT / "contracts" / "README.md"]
    for doc in docs:
        text = re.sub(r"```.*?```", "", doc.read_text(), flags=re.S)  # ignore code blocks
        for target in re.findall(r"\]\(([^)\s]+)\)", text):
            if re.match(r"^(https?:|mailto:|#)", target):
                continue
            path = target.split("#", 1)[0]
            if path and not (doc.parent / path).resolve().exists():
                errors.append(f"{doc.relative_to(ROOT)}: broken link -> {target}")
    return errors


def check_migrations() -> list[str]:
    chapter = (HANDBOOK / "08-data-model.md").read_text()
    migrations = sorted(p.name for p in (ROOT / "ingestion-service/src/main/resources/db/migration").glob("V*.sql"))
    return [f"08-data-model.md: migration {m} is not documented" for m in migrations if m not in chapter]


def env_vars() -> set[str]:
    found: set[str] = set()
    for p in [*files("*.py", ROOT / "agent-service" / "app"), ROOT / "mcp-tools/observability/server.py",
              ROOT / "chaos-lab/service/main.py"]:
        found |= set(re.findall(r'os\.getenv\("([A-Z0-9_]+)"', p.read_text()))
    for p in [*files("*.ts", ROOT / "web"), *files("*.tsx", ROOT / "web")]:
        found |= set(re.findall(r"process\.env\.([A-Z0-9_]+)", p.read_text()))
    for p in files("application.yml"):
        found |= set(re.findall(r"\$\{([A-Z][A-Z0-9_]+):", p.read_text()))
    return found - {"NODE_ENV"}


def check_env_vars() -> list[str]:
    reference = (HANDBOOK / "13-deployment-and-operations.md").read_text()
    return [f"13-deployment-and-operations.md: env var {v} missing from the configuration reference"
            for v in sorted(env_vars()) if v not in reference]


def metrics() -> set[str]:
    found: set[str] = set()
    for p in [*files("*.py", ROOT / "agent-service" / "app")]:
        found |= set(re.findall(r'(?:Counter|Histogram|Gauge)\(\s*"([a-z_]+)"', p.read_text()))
    for p in [*files("*.java", ROOT / "gateway-service" / "src" / "main"),
              *files("*.java", ROOT / "ingestion-service" / "src" / "main")]:
        for name in re.findall(r'(?:counter|timer)\("(sentinel\.[a-z.]+)"', p.read_text()):
            found.add(name.replace(".", "_"))  # Prometheus naming
    return found


def check_metrics() -> list[str]:
    catalog = (HANDBOOK / "12-observability.md").read_text()
    return [f"12-observability.md: metric {m} missing from the catalog" for m in sorted(metrics()) if m not in catalog]


def check_diagrams() -> list[str]:
    errors = []
    for src in sorted((HANDBOOK / "diagrams").glob("*.mmd")):
        for ext in ("svg", "png"):
            img = HANDBOOK / "images" / f"{src.stem}.{ext}"
            if not img.exists():
                errors.append(f"diagram {src.name} has no rendered {ext} (cd docs/build && node diagrams.mjs)")
            # mtimes are meaningful only in a working copy; a fresh CI checkout stamps every file alike
            elif not os.getenv("CI") and img.stat().st_mtime < src.stat().st_mtime:
                errors.append(f"diagram {src.name} changed after images/{img.name} was rendered (node diagrams.mjs)")
    return errors


def main() -> int:
    errors = check_links() + check_migrations() + check_env_vars() + check_metrics() + check_diagrams()
    for e in errors:
        print(f"DOCS: {e}")
    print("docs check: " + ("OK" if not errors else f"{len(errors)} problem(s)"))
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
