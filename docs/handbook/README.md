# Sentinel engineering handbook

The complete reference for Sentinel: what it is, how it's designed (high level and low level), why each decision was made, how it's tested, deployed and operated, and what happened in each phase of building it.

> **Status:** current as of **Phase 16: Two people (2026-10-25)**. Every chapter is updated in the same change as the code it describes. See [Maintaining this handbook](#maintaining-this-handbook).

## How to read it

| If you want to… | Read |
|---|---|
| Understand the product in 10 minutes | [01 Overview](01-overview.md) |
| Understand the architecture | [02 High-level design](02-high-level-design.md) |
| Work on a specific service | The matching low-level design: [03 Gateway](03-lld-gateway.md) · [04 Agent](04-lld-agent.md) · [05 Ingestion & knowledge](05-lld-ingestion-and-knowledge.md) · [06 MCP tools & chaos-lab](06-lld-mcp-tools-and-chaos-lab.md) · [07 Web console](07-lld-web-console.md) |
| Know what's stored where | [08 Data model](08-data-model.md) |
| Call or change an API | [09 APIs & contracts](09-api-and-contracts.md) |
| Review security | [10 Security](10-security.md) |
| Change prompts, models, rules or guardrails | [11 Evaluation](11-evaluation.md) (the benchmark gate decides; SafeActionBench in §2A) |
| Debug in production | [12 Observability](12-observability.md) and [13 Deployment & operations](13-deployment-and-operations.md) |
| Add or change tests / CI | [14 Testing & CI](14-testing-and-ci.md) |
| Know how we got here and what broke along the way | [15 Phase history](15-phase-history.md) |
| Know what's missing | [16 Limitations & roadmap](16-limitations-and-roadmap.md) |
| Use or change Sentinel Guard (v2) | [17 Sentinel Guard](17-lld-sentinel-guard.md) and the [user guide](../guard/README.md) |

**Single-file versions** (every chapter, figure and screenshot in one document):
- [`docs/Sentinel-Handbook.pdf`](../Sentinel-Handbook.pdf): A4, cover, contents, page numbers.
- [`Sentinel-Handbook.html`](Sentinel-Handbook.html): self-contained, opens in any browser.

Shorter companions: the [user guide](../USER-GUIDE.md), the [system design summary](../DESIGN.md), the [demo script](../DEMO.md), and the sixteen [architecture decision records](../adr/).

## Figures
The handbook has **79 diagrams and charts** (architecture, sequence, state, ER, deployment, flowcharts, data charts) and **17 screenshots of the running system** (console incl. the closed loop, Jaeger, Grafana, Prometheus targets and alerts, and the guard's approvals inbox and dashboard). Every figure is numbered and captioned.

| Kind | Source (edit this) | Rendered to | How |
|---|---|---|---|
| Diagrams and charts | `diagrams/<chapter>-<name>.mmd` ([Mermaid](https://mermaid.js.org/)) | `images/<name>.svg` (vector, used in the PDF/HTML) and `images/<name>.png` (2×, embedded in the Markdown) | `cd docs/build && npm ci && node diagrams.mjs` |
| Screenshots | the running system | `images/screens/*.png` | `scripts/dev-local.sh up`, seed, inject a fault, then `node screenshots.mjs` (`SECTIONS=closedloop,list,grafana,…` captures only some; `SKIP_INCIDENT=1` skips the slow LLM incident) |
| PDF + HTML book | the chapters | `../Sentinel-Handbook.pdf`, `Sentinel-Handbook.html` | `node book.mjs` |

Markdown embeds PNGs so the figures show in any viewer: GitHub, IDEs, the Claude app, file previews. The build uses the installed Chrome (no browser download; override with `CHROME_PATH`).

## Chapter map

| # | Chapter | Covers |
|---|---|---|
| 01 | [Overview](01-overview.md) | Problem, capabilities, glossary, technology stack, repository map, phase timeline |
| 02 | [High-level design](02-high-level-design.md) | Requirements, context and container views, the core flows (incl. approval → execution → verification and alert intake), deployment views, cross-cutting design |
| 03 | [LLD: gateway-service](03-lld-gateway.md) | Packages, identity (ES256 key ring, JWKS, issuer-routed verification), security filter chain, rate limiting, incidents and decisions, integrations, alert webhook (dedupe, correlation), capabilities and audit, knowledge, RAG `/ask`, semantic cache, MCP server, tool tokens |
| 04 | [LLD: agent-service](04-lld-agent.md) | LangGraph state machine, gateway-token authentication, checkpoint RLS, investigation loop and budgets, execute and verify nodes, timeline, LLM providers, prompts, rules, guardrails, MCP toolbox, checkpoints |
| 05 | [LLD: ingestion & knowledge](05-lld-ingestion-and-knowledge.md) | Kafka contract, chunking, embeddings, atomic replace, DLT, hybrid retrieval, migrations runner |
| 06 | [LLD: MCP tools & chaos-lab](06-lld-mcp-tools-and-chaos-lab.md) | Observability MCP server (incl. `probe_service`), the remediation adapter and its capability checks, wire shapes, fault injection, the ops API, loadgen and alert rules |
| 07 | [LLD: web console](07-lld-web-console.md) | Backend-for-frontend, OIDC code flow with PKCE, sessions and refresh, proxy allow-list, pages, the closed-loop panel, integrations admin |
| 08 | [Data model](08-data-model.md) | Every table, column, index, RLS policy, role and migration |
| 09 | [APIs & contracts](09-api-and-contracts.md) | Every endpoint with request/response, MCP tools, Kafka events, headers, error mapping |
| 10 | [Security](10-security.md) | Threat model, trust boundaries, controls, tenancy, guardrails, secrets |
| 11 | [Evaluation](11-evaluation.md) | RCA benchmark design, metrics, gate, results history, retrieval eval |
| 12 | [Observability](12-observability.md) | Traces, metrics catalog, dashboard, logs |
| 13 | [Deployment & operations](13-deployment-and-operations.md) | Local, compose, kind, AWS; configuration reference; runbooks and troubleshooting |
| 14 | [Testing & CI](14-testing-and-ci.md) | Test pyramid per service, what each suite proves, CI jobs |
| 15 | [Phase history](15-phase-history.md) | Each phase: goals, what was built, decisions, bugs found and fixed, results |
| 16 | [Limitations & roadmap](16-limitations-and-roadmap.md) | Known gaps, risks, and what comes next |
| 17 | [LLD: Sentinel Guard](17-lld-sentinel-guard.md) | v2: admission webhooks, request lifecycle, approval binding, exactly-once under API-server retries, signed audit chain, Kubernetes state, measured latency |

## Conventions
- **Paths** are relative to the repository root, e.g. `agent-service/app/graph.py`.
- **"Tenant"** always means the `tenant_id` claim of a verified JWT. It is never read from a request body or URL supplied by a user.
- **Numbers** (test counts, benchmark scores, latencies) are measured, never estimated. Each one names its source.
- Diagrams are [Mermaid](https://mermaid.js.org/) and render on GitHub.

## Maintaining this handbook
Documentation is part of the change, not a follow-up. Any change to behavior, configuration, APIs, schema, deployment or evaluation results updates the affected chapters **in the same commit**:

| You changed… | Update |
|---|---|
| An endpoint, request/response, header, event or MCP tool | 09, plus the service's LLD |
| Schema (a new Flyway migration) | 08 (tables, policies, migration list) |
| A config property or environment variable | 13 (configuration reference) and the service's LLD |
| Prompts, rules, guardrails, models, budgets | 04 and 11; re-run the benchmark and record the result |
| A metric, span or dashboard panel | 12 |
| Helm values, Terraform, scripts | 13 |
| Tests or CI jobs | 14 |
| Anything notable, including bugs found | 15 (current phase entry) and, if it's a gap, 16 |
| A significant design decision | Add an ADR in `docs/adr/` and link it from 02 |
| Architecture, a flow, or anything a figure shows | Edit `diagrams/*.mmd` (or add one and embed `images/<name>.png` with a numbered caption), run `node diagrams.mjs` |
| The console UI | Re-capture: `node screenshots.mjs` |
| Anything above (before you finish) | Rebuild the single-file book: `node book.mjs` |

The repository's `CLAUDE.md` and pull-request template carry the same rule, and CI enforces the mechanical part: `scripts/check_docs.py` fails the build if a link or image breaks, if a migration, environment variable or metric exists in code without being documented here, or if a diagram source is newer than its rendered image.
