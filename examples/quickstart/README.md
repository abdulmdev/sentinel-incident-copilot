# Quickstart: Sentinel Guard with a live agent

```bash
docker compose up                         # from the repository root
docker compose --profile monitoring up    # also Prometheus and Grafana
```

| Open | What you'll see |
|---|---|
| http://localhost:8080 | The approvals inbox. Sign in with the approver token **`approve-me`** and your name (it goes into the audit trail) |
| `docker compose logs -f agent` | The agent's side: what it asked, what the guard answered, what it did |
| http://localhost:9090/alerts | *(monitoring)* Prometheus with the chart's guardian alerts and recording rules |
| http://localhost:3000 | *(monitoring)* Grafana, opening on the Sentinel Guard dashboard |

## What runs
- **`guard`:** Sentinel Guard in single-process mode, built from [`guard/`](../../guard). It uses the walkthrough's policy ([`examples/walkthrough/policy.yaml`](../walkthrough/policy.yaml)):

  | Action | Policy |
  |---|---|
  | `refund` | high risk: a person must approve |
  | `send-receipt` | low risk: runs on policy (`observe`), and is still recorded |
  | `delete-customer-data` | never allowed for an agent |

- **`agent`:** [`agent.py`](agent.py), a support desk on the [Python SDK](../../sdk/python). It works through a ticket every 20 s (the first three come at once), with at most three refunds waiting for a person at a time:
  - For each refund, it waits for your decision. Once you approve, it redeems the single-use capability and issues the refund, once.
  - After every third approval, it tries the spent capability again, to show the guard refusing it (`409`).
  - It hands erasure requests to a person, because the guard blocks them.
- **`prometheus`, `grafana`** (profile `monitoring`):
  - Prometheus scrapes the guard every 5 s and loads [`guard-alerts.yaml`](../../deploy/helm/sentinel-guard/alerts/guard-alerts.yaml), the same file the Helm chart ships.
  - Grafana shows [`sentinel-guard.json`](../../deploy/helm/sentinel-guard/dashboards/sentinel-guard.json), also the chart's.

Try denying a few refunds, or set `TICKET_EVERY=3`. Within minutes the drift alerts start to fire, for example `SentinelGuardAgentBlockedBurst` from the erasure attempts.

## Settings
Set any of these in the environment or a `.env` file next to `compose.yaml`:

| Variable | Default | |
|---|---|---|
| `APPROVER_TOKEN` | `approve-me` | the inbox's sign-in (the guard's admin token) |
| `AGENT_TOKEN` | `agent-demo-token` | the agent's token |
| `TICKET_EVERY` | `20` | seconds between tickets |
| `GUARD_PORT`, `PROMETHEUS_PORT`, `GRAFANA_PORT` | `8080`, `9090`, `3000` | host ports, bound to 127.0.0.1 only |

The tokens are for trying it on your machine. The guard keeps its state in memory: `docker compose restart guard` starts a fresh record, and the agent asks again for what it was waiting on.

## Checked in CI
[`check.py`](check.py) runs against `docker compose --profile monitoring up` in CI, and on your machine:

```bash
TICKET_EVERY=3 docker compose --profile monitoring up -d --build --wait
uv run --with httpx python examples/quickstart/check.py --monitoring
docker compose --profile monitoring down
```

It checks 18 things:
- the inbox and its security headers;
- each path the agent takes;
- an approval leading to exactly one execution, and a reused capability refused;
- a denial reaching the agent with its note;
- the audit chain and the evidence export;
- Prometheus's rules and scrape;
- Grafana's dashboard.

Next: the scripted [walkthrough](../walkthrough/README.md) (offline evidence verification and tampering), or [Kubernetes](../../docs/guard/README.md#install-on-kubernetes).
