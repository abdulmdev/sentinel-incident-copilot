# 3-minute demo script

Setup (before recording): `scripts/dev-local.sh up`, then `python3 scripts/seed_knowledge.py acme`.
Open four tabs: console http://localhost:3000, Alertmanager http://localhost:9093, Jaeger http://localhost:16686, Grafana http://localhost:3001.
Loadgen is already sending traffic, so the injected fault reaches the metrics.

| Time | Show | Say |
|---|---|---|
| 0:00 | README architecture diagram | "Sentinel is an on-call copilot: an alert opens the incident, an agent investigates with tools and cites its evidence, and nothing changes production without a human. Once I approve, it fixes the problem and proves it worked." |
| 0:15 | Terminal: `python3 chaos-lab/inject.py checkout bad_deploy` | "I'll break a real service with a bad deploy. I'm not opening an incident myself." |
| 0:25 | Alertmanager tab → `HighErrorRatio` firing; console list → a new row with source **alert** | "Prometheus fired, Alertmanager called Sentinel with an integration token that can only post alerts, and the incident opened itself, dated when the errors started." |
| 0:40 | Console → **Sign in with Keycloak** as alice@acme | "Real OIDC: the console runs the code flow server-side and never shows the token to the browser. My tenant comes from the identity provider, and I can't change it myself." |
| 0:45 | Incident page: rollback proposal, two hypotheses, citations, evidence | "Every claim cites evidence it gathered, here the deploy record, the NullPointerException, and our own postmortem of the same bug." |
| 1:00 | Click **Approve** → "Executing and verifying…" → **Closed loop** panel | "Approving mints a capability: one rollback, of checkout, once, for five minutes. The adapter checks every claim and burns the token in Redis before it touches anything. Then the agent probes the service. Healthy: mitigated in about 30 seconds from the first error." |
| 1:15 | Jaeger: the decision trace | "agent.execute, the remediation call, then verify with its probes, all in one trace. Tracing is also how we found the agent making 50 MCP calls for 6 tool calls, and cut it to 26." |
| 1:25 | Integrations page | "Alertmanager's credential was issued by the gateway and is revocable. Only the gateway can sign service tokens; the agent and the adapter just verify them, so a compromised agent can't forge an approval." |
| 1:30 | Grafana: Closed loop row | "Alerts opened versus deduplicated or correlated, remediation outcomes, time to mitigate." |
| 1:50 | `evals/rca/REPORT.md` history table | "112 replayed incidents, scored deterministically in CI. The eval caught our fallback rolling back harmless deploys 16% of the time. It's 0% now, and the gate fails the build if it regresses." |
| 2:15 | Report: injection tier, unguarded vs. guarded | "A local model fell for a prompt injection in the logs and proposed a rollback. The policy interlock blocked it. Input filters can be bypassed; the output policy can't." |
| 2:40 | Terminal: `kubectl -n sentinel get pods` (kind) | "Same system on Kubernetes via Helm: hardened pods, migrations as a hook job, NetworkPolicies verified. Terraform for EKS, RDS, MSK and ElastiCache." |
| 2:55 | End | "Code, ADRs and the benchmark are all in the repo." |
