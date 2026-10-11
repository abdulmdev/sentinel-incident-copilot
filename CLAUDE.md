# Sentinel: working agreement

Agentic incident-response copilot. Start with `docs/handbook/README.md` (HLD, LLD per service, data model, APIs, security, evals, operations, phase history).

## Documentation is part of every change (standing rule)
Keep `docs/handbook/` exactly in sync with the code, **in the same change**, for every phase and every behavior change, not as a follow-up. The table in `docs/handbook/README.md` ("Maintaining this handbook") says which chapter to update for which kind of change. In particular:
- A new phase or notable change → append to `docs/handbook/15-phase-history.md` (goal, built, decisions, bugs found and fixed, measured results).
- Endpoint, header, event or MCP tool changes → `09-api-and-contracts.md` + the service's LLD chapter.
- Schema (a new Flyway migration) → `08-data-model.md`.
- Config / env var → `13-deployment-and-operations.md` configuration reference.
- Prompts, rules, guardrails, models, budgets → `04-lld-agent.md` + `11-evaluation.md` (re-run the benchmark; record results).
- Metrics, spans, dashboards → `12-observability.md`. Tests or CI → `14-testing-and-ci.md` (keep counts exact).
- New gaps → `16-limitations-and-roadmap.md`. Significant decisions → a new ADR in `docs/adr/`, linked from `02-high-level-design.md`.
- Update the status line at the top of `docs/handbook/README.md`.
- Numbers in docs must be measured, never estimated.
- Figures: diagrams live in `docs/handbook/diagrams/*.mmd`; render with `cd docs/build && node diagrams.mjs` and embed `images/<name>.png` with a numbered caption. Refresh UI screenshots with `node screenshots.mjs` when the console changes.
- Rebuild the single-file book (`docs/Sentinel-Handbook.pdf` + `docs/handbook/Sentinel-Handbook.html`) with `node book.mjs`.
- Run `python3 scripts/check_docs.py` before finishing (CI runs it too).

## Verifying changes
- Java: `cd gateway-service && mvn verify`, `cd ingestion-service && mvn verify` (Testcontainers: Docker needed).
- Python: `cd agent-service && uv run pytest`, `cd mcp-tools/observability && uv run pytest`.
- Behavior gate: `uv run --project agent-service python evals/rca/run.py --check` (+ `--scenarios scenarios-holdout.jsonl`).
- Console: `cd web && npm run build`. Chart: `.tools/helm lint deploy/helm/sentinel`. Terraform: `.tools/terraform -chdir=deploy/terraform/aws validate`.
- Live: `scripts/dev-local.sh up` (fast loop) or `scripts/kind-up.sh` (Kubernetes).
