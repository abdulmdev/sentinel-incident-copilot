# Sentinel user guide

## What Sentinel is for
When something breaks in production, an on-call engineer loses the first 10–30 minutes gathering evidence: what was deployed, what the logs say, what the metrics show, what the runbook recommends. Then they decide on a fix, apply it, and check it worked.

Sentinel does that loop for you, with a human in control:
1. **An alert opens an incident** (from Prometheus/Alertmanager), one per real problem.
2. **An AI agent investigates.** It calls tools for deploys, error logs, metrics and runbooks, then proposes a root cause and **one** fix. Every claim cites the evidence it came from.
3. **You approve or reject.** Nothing touches production before a human says yes.
4. **Sentinel applies exactly the approved fix, once**, then **checks the service really recovered** and records how long it took (time to mitigate).
5. **It writes the postmortem**: timeline, evidence, decision, outcome.

It also answers on-call questions from your team's runbooks ("Ask"), with citations.

Who it's for:
- on-call/SRE teams who want faster, safer incident response;
- as a portfolio project, a demonstration of production-grade AI engineering: agents, RAG, evals, guardrails, security, observability, Kubernetes.

## Start it (local, about 3 minutes)
Prerequisites: Docker Desktop, Java 21+, Maven, [uv](https://docs.astral.sh/uv/), Node 24. Optional: [Ollama](https://ollama.com) for a free local LLM.

```bash
scripts/dev-local.sh up
```

```bash
python3 scripts/seed_knowledge.py acme
```

The first command starts everything: databases, Keycloak, the services, monitoring and the console. The second loads the demo runbooks.

To use a local LLM instead of the built-in rules (`ollama pull gemma4` once):

```bash
SENTINEL_LLM_PROVIDER=ollama scripts/dev-local.sh up
```

Stop the services (Docker infrastructure keeps running):

```bash
scripts/dev-local.sh down
```

| Open | URL |
|---|---|
| Console | http://localhost:3000 |
| Keycloak (sign-in, user admin) | http://localhost:8180 |
| Alerts (Alertmanager) | http://localhost:9093 |
| Traces (Jaeger) | http://localhost:16686 |
| Dashboard (Grafana) | http://localhost:3001 |
| Prometheus | http://localhost:9090 |

## Use it: the 5-minute tour
1. **Sign in.** At http://localhost:3000 click **Sign in with Keycloak**. The dev users are `alice@acme` (tenant admin), `bob@acme` and `carol@globex`; their test passwords are in `deploy/keycloak/sentinel-realm.json`. There's also a one-click *dev sign-in* for local use.
2. **Break something:**

   ```bash
   python3 chaos-lab/inject.py checkout bad_deploy
   ```

   Other faults: `db_pool_exhaustion`, `memory_leak`, `latency`, `errors`.
3. **Watch the incident open itself.** Within about 40 s, Prometheus fires `HighErrorRatio`, Alertmanager calls Sentinel, and a new row appears on **Incidents** with source *alert*.
4. **Review it.** Open the incident to see the proposed fix (e.g. *rollback → checkout, risk low*), the ranked hypotheses with citation chips, every piece of evidence, and any guardrail interventions.
5. **Approve.** Click **Approve**. The *Closed loop* panel shows the execution result, the verification probes and **"mitigated in N s"**. **Reject** closes the incident without doing anything.
6. **Read the postmortem** at the bottom of the incident page. It's written automatically.
7. **Ask questions:** **Ask** → e.g. *"How do I handle DB connection pool exhaustion?"* The answer is grounded in the runbooks, with numbered citations; asking again hits the cache.
8. **Search knowledge:** **Knowledge** compares vector, keyword and hybrid search side by side.
9. **Manage integrations** (tenant admins): **Integrations** issues or revokes the Alertmanager token. Revoking takes effect on the next alert.
10. **Look under the hood:**
    - **Jaeger:** one trace per incident or decision (agent steps, tool calls, the remediation, the probes);
    - **Grafana:** incident outcomes, time to mitigate, LLM spend, guardrails;
    - **Prometheus → Alerts:** the rules.

Open an incident by hand: the form at the top of **Incidents**.

## Use it from the API
User tokens come from Keycloak, or from the dev script for local scripting:

```bash
TOKEN=$(python3 scripts/dev_token.py alice@acme acme)
```

```bash
curl -s localhost:8080/api/v1/incidents -H "Authorization: Bearer $TOKEN"
```

Full API reference: [handbook chapter 09](handbook/09-api-and-contracts.md).

## Use your own services and runbooks
- **Runbooks:** `POST /api/v1/knowledge/documents` (or put Markdown in `knowledge/<tenant>/` and run `seed_knowledge.py <tenant>`).
- **Alerts:** point your Alertmanager at `…/api/v1/alerts/alertmanager` with a token from **Integrations**. Every alert needs a `service` label.
- **Telemetry and fixes:** the agent reads deploys, logs and metrics, and applies fixes, through two MCP servers (`mcp-tools/observability`, `mcp-tools/remediation`). To use real systems, implement the same tool contracts in front of your log store, metrics, deploy system and ops API ([chapter 06](handbook/06-lld-mcp-tools-and-chaos-lab.md)). The agent doesn't change.

## Run it on Kubernetes
To try it on a local cluster:

```bash
scripts/kind-up.sh
```

The console is then at http://localhost:13000 and the API at http://localhost:18080. For AWS, see `deploy/terraform/aws` and [chapter 13](handbook/13-deployment-and-operations.md).

## Check quality
| What | Command |
|---|---|
| Benchmark (112 replayed incidents, ~1 s, rules baseline) | `uv run --project agent-service python evals/rca/run.py --check` |
| The same benchmark with a local LLM (~1 h) | `uv run --project agent-service python evals/rca/run.py --mode llm --provider ollama` |
| All tests | [chapter 14](handbook/14-testing-and-ci.md); CI runs them on every push |

## Where to read more
- [Engineering handbook](handbook/README.md), also as a [PDF](Sentinel-Handbook.pdf): architecture, every service's design, security, operations, and the full build history.
- [Demo script](DEMO.md) for a 3-minute walkthrough.
