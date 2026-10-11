# ADR-0005: Offline, deterministic evals as a CI gate

**Status:** accepted, 2026-10-05

## Context
We need to know whether a change makes Sentinel better or worse at diagnosing incidents. That covers prompt edits, model swaps, tool changes and rule changes. Live end-to-end runs (Docker, chaos-lab, real model) are slow, flaky and costly, and they test one scenario at a time. An LLM-as-judge adds its own variance and cost, and it's hard to trust as a merge gate.

## Decision
- **Replay, don't simulate.** Incidents are recorded tool outputs replayed through MCP-shaped tools with the production wire shapes. The real agent graph runs unchanged, so the eval covers parsing, the investigation loop, budgets and fallbacks, not just a prompt.
- **Score with exact checks.** Remediation and cause classes are enums. Citations are checked against the evidence actually gathered. "Harmful" (wrong *and* disruptive) is tracked separately from "wrong", because a wrong escalation costs minutes and a wrong rollback can cause an outage.
- **Gate CI on the baseline.** The rule-based path is deterministic, so its thresholds are exact. Its gate guards the production fallback (what runs when the LLM is down) and the harness itself.
- **Make the LLM run opt-in and budgeted.** It's a manual workflow with a spend cap checked before starting, held to "at least as good as the baseline, at least as safe".
- **Version the benchmark.** The generator is seeded and committed, and CI checks that `scenarios.jsonl` regenerates byte-for-byte. A held-out seed checks generalization to unseen instances.

## Consequences
- + Runs in about a second for 100 incidents, needs no network or key, and gives the same answer every time.
- + It already found a real bug: the fallback rolled back a harmless deploy in every red-herring incident (16% harmful overall). Fixed by applying the runbook's "errors within 30 minutes of the deploy" rule. The record: 76% → 92% remediation accuracy, 16% → 0% harmful, identical on the held-out set.
- − Synthetic data has the author's blind spots. Mitigation: grow the set from recorded chaos-lab runs and real postmortems, and keep the remaining baseline misses (metrics-only diagnosis with logs down) as the target the LLM path must beat, not something to tune rules against.
- − Replay can't test the live MCP transport or Postgres. Those are covered by the integration tests and the live demo.
