# Quickstart: Sentinel Guard with a live agent

```bash
docker compose up                         # from the repository root
docker compose --profile monitoring up    # also Prometheus and Grafana
```

| Open | What you'll see |
|---|---|
| http://localhost:8080 | The inbox. Sign in as Alice (**`alice-demo`**) or Bob (**`bob-demo`**): the guard records each decision as the person whose token it is. It opens on an **Overview** (what waits, what ran, what was blocked, each agent, the activity); click any request for its details and signed audit trail |
| `docker compose logs -f agent` | The agent's side: what it asked, what the guard answered, what it did |
| `docker compose logs -f notifications` | What Slack would receive: each request that needs someone, with a link to the inbox |
| http://localhost:9090/alerts | *(monitoring)* Prometheus with the chart's guardian alerts and recording rules |
| http://localhost:3000 | *(monitoring)* Grafana, opening on the Sentinel Guard dashboard |

## What runs
- **`guard`:** Sentinel Guard in single-process mode, built from [`guard/`](../../guard), with two approvers, each with their own token, and [`policy.yaml`](policy.yaml):

  | Action | Policy |
  |---|---|
  | `refund` | high risk: a person must approve |
  | `close-account` | high risk: **two different people** must approve (`approvals: 2`) |
  | `send-receipt` | low risk: runs on policy (`observe`), and is still recorded |
  | `delete-customer-data` | never allowed for an agent |

- **`agent`:** [`agent.py`](agent.py), a support desk on the [Python SDK](../../sdk/python). It works through a ticket every 20 s (the first four come at once), with at most three requests waiting for people at a time:
  - For each refund, it waits for your decision. Once you approve, it redeems the single-use capability and issues the refund, once.
  - For an account closure, it waits for two people; its capability then names both.
  - After every third approval, it tries the spent capability again, to show the guard refusing it (`409`).
  - It hands erasure requests to a person, because the guard blocks them.
- **`notifications`:** [`notify_sink.py`](notify_sink.py), standing in for a Slack webhook. It prints each notice the guard sends.
- **`prometheus`, `grafana`** (profile `monitoring`):
  - Prometheus scrapes the guard every 5 s and loads [`guard-alerts.yaml`](../../deploy/helm/sentinel-guard/alerts/guard-alerts.yaml), the same file the Helm chart ships.
  - Grafana shows [`sentinel-guard.json`](../../deploy/helm/sentinel-guard/dashboards/sentinel-guard.json), also the chart's.

Try denying a few refunds, or set `TICKET_EVERY=3`. Within minutes the drift alerts start to fire, for example `SentinelGuardAgentBlockedBurst` from the erasure attempts.

## Settings
Set any of these in the environment or a `.env` file next to `compose.yaml`:

| Variable | Default | |
|---|---|---|
| `ALICE_TOKEN`, `BOB_TOKEN` | `alice-demo`, `bob-demo` | the two approvers' personal tokens |
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

It checks 29 things:
- the inbox, its security headers, and who a token belongs to;
- each path the agent takes;
- an approval leading to exactly one execution, and a reused capability refused;
- nobody deciding under someone else's name;
- a denial reaching the agent with its note;
- the account closure: Alice's approval keeps it waiting, Alice can't count twice, and Bob's approval decides it;
- the notifications, each linking straight to its request;
- the overview's counts and the chain's health;
- the audit chain and the evidence export;
- Prometheus's rules and scrape;
- Grafana's dashboard.

Next: the scripted [walkthrough](../walkthrough/README.md) (offline evidence verification and tampering), or [Kubernetes](../../docs/guard/README.md#install-on-kubernetes).
