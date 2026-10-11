# Evals

Two benchmarks, both scored by deterministic checks (no LLM judge):

| Benchmark | Measures | Needs | Runs in CI |
|---|---|---|---|
| [`rca/`](rca/) | End-to-end incident diagnosis: 100 replayed incidents through the real agent graph | nothing (offline) | yes, as a gate |
| [`retrieval/`](retrieval/) | Runbook search quality: recall@k, MRR for vector vs keyword vs hybrid | the running stack | no |

## RCA benchmark

```bash
uv run --project agent-service python evals/rca/run.py --check
```

**What it is.** [`generate.py`](rca/generate.py) builds 100 incidents (seeded, committed) across 8 failure families and 5 difficulty tiers. Each holds what the observability tools return for each service, plus labels: correct remediations, correct root-cause classes, relevant runbooks. [`replay.py`](rca/replay.py) serves that telemetry through MCP-shaped tools, with the same wire shapes as the real servers. The agent under test runs its real graph: investigation loop, budgets, triage, RCA.

**Tiers** are designed to find where an agent breaks:
- `clean`
- `noisy`: unrelated warnings
- `red_herring`: a harmless deploy hours earlier
- `missing_data`: the log backend is down
- `subtle`: a bad deploy with only generic 500s

On top of the tiers, there are `cascade` incidents (the fault is in a downstream service) and novel faults no rule covers, where escalating is the right answer.

**Metrics** ([`scoring.py`](rca/scoring.py), pinned by [`test_scoring.py`](rca/test_scoring.py)):

| Metric | Definition |
|---|---|
| Remediation accuracy | Proposed action (and target, when labeled) is one of the correct ones |
| **Harmful action rate** | Wrong *and* disruptive (rollback/restart/failover/pool change). Wrong-but-escalated is not harmful |
| Root-cause / triage accuracy | Top cause class / triage category matches the label |
| Escalation recall | Escalates when nothing in the evidence supports a confident fix |
| Citation faithfulness | Every citation points at evidence actually gathered in that run: no made-up sources |
| Grounded rate | When a cause is named, the lead hypothesis cites telemetry, not only a runbook |
| Runbook citation precision | Cited runbooks are relevant to the incident |

**Gate.** [`thresholds.json`](rca/thresholds.json) holds per-mode bounds, and `--check` exits 1 when any metric crosses them. The baseline is deterministic, so its bounds are exact. Simulating the original deploy bug drops the scores to 76% / 16% and fails the gate (verified).

**Claude.** Run `--mode llm --max-usd <budget>` with `ANTHROPIC_API_KEY` set; the CI job is manual (`workflow_dispatch`). It refuses to start if the worst-case spend exceeds `--max-usd`. Results land beside the baseline in [`REPORT.md`](rca/REPORT.md).

### Honest limitations
- **The labels and the generator have one author (us).** A held-out set (`--seed 7`) checks that fixes generalize to unseen *instances*, but it comes from the same generator, so it can't catch blind spots in the generator itself. The next step is to add incidents recorded from chaos-lab and from real postmortems.
- **Offline runbook search** is BM25 over the same files, not the production hybrid search. Retrieval quality is measured separately in `retrieval/`.
- **No LLM-as-judge** for free-text quality (postmortem writing). Everything gated here is exact.
