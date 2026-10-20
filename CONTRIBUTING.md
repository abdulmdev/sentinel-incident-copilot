# Contributing to Sentinel

Thanks for helping. Sentinel is an incident-response copilot that can act on production, so changes are held to two rules: **safety stays measurable** and **the docs stay true**.

## Before you start
- Read the [engineering handbook](docs/handbook/README.md): the high-level design (ch. 02), the service you're touching (ch. 03–07) and security (ch. 10).
- For anything larger than a fix, open an issue first to agree on the approach.

## Run it locally
```bash
scripts/dev-local.sh up
```
Details, including the Keycloak dev users: [user guide](docs/USER-GUIDE.md).

## Verify your change
| Area | Command |
|---|---|
| Gateway / ingestion (needs Docker for Testcontainers) | `cd gateway-service && mvn verify` · `cd ingestion-service && mvn verify` |
| Agent, MCP servers | `cd agent-service && uv run pytest` · `cd mcp-tools/observability && uv run pytest` · `cd mcp-tools/remediation && uv run pytest` |
| **Behavior gate** (prompts, rules, guardrails, models) | `uv run --project agent-service python evals/rca/run.py --check`, plus `--scenarios scenarios-holdout.jsonl` |
| Console | `cd web && npm run build` |
| Chart / infra | `.tools/helm lint deploy/helm/sentinel` · `terraform -chdir=deploy/terraform/aws validate` |
| Docs | `python3 scripts/check_docs.py` |

CI runs all of these, plus a real Kubernetes (kind) end-to-end test.

## Rules that matter here
- **Docs in the same change.** Behavior, API, schema, config, metric or test changes update the matching handbook chapter ([which chapter](docs/handbook/README.md#maintaining-this-handbook)). CI checks links, migrations, env vars and metrics.
- **Safety changes are measured.** A change to prompts, rules, guardrails or models re-runs the benchmark, and results go in [chapter 11](docs/handbook/11-evaluation.md). Never tune rules to pass a specific benchmark case; encode a runbook policy instead.
- **Numbers in docs are measured, never estimated.**
- **Nothing executes without approval.** A new remediation needs a capability check, a runbook precondition, tests for refusal paths, and an ADR if it changes the trust model.
- **Tenant comes from the token**, never from a request body; new tables get row-level security (`FORCE`).

## Pull requests
Keep them focused; describe what changed and how you verified it. The PR template has the checklist.

By contributing you agree that your contributions are licensed under the [Apache License 2.0](LICENSE), and that you'll follow the [code of conduct](CODE_OF_CONDUCT.md).
