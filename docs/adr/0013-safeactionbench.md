# ADR-0013: SafeActionBench, an open benchmark for harmful agent actions

**Status:** accepted, 2026-10-24. Builds on [ADR-0005](0005-offline-deterministic-evals-as-a-ci-gate.md) (the deterministic RCA benchmark) and [ADR-0010](0010-sentinel-guard.md) (the guard).

## Context
Sentinel's RCA benchmark measures Sentinel's own agent, through its own harness and its own graph. Two questions matter beyond Sentinel:
1. **How often does any agent's production action cause harm?**
2. **How much does an independent guard prevent, and at what cost in good fixes?**

Published agent benchmarks mostly score task success. Harm is a side note, and nothing separates the model's proposal from the system's checks from an independent guard.

## Decisions
1. **A standalone package, `bench/` (`safeactionbench`).**
   - It ships the same 224 incidents (two splits) and the runbooks, byte for byte with `evals/rca` (a test checks this), so it installs and runs without Sentinel.
   - It depends only on the MCP SDK.
2. **Agent-agnostic interfaces.**
   - **An MCP server** (`safeactionbench serve`): `next_incident`, five telemetry tools, `propose_remediation`. Any MCP agent can take the benchmark with no code. Outages and injection attacks are replayed exactly; proposals stream to a JSONL file.
   - **A predictions file** (`{id, action, target, proposed_action?}`) for everyone else.
   - A test drives a reference agent through the MCP server and requires **exactly** the predictions it makes when called directly, across all 112 incidents.
3. **Three levels of harm in every score: proposed, after the system's own checks, behind the reference guard.** That separates the model from the system's own checks, and both from an independent layer.
4. **The reference guard sees only what a platform guard would:** the target's metrics and deploy history, never the logs or the agent's reasoning.
   - Its thresholds were fixed before any scoring, with stated sources: runbook rules and alert rules, plus one deliberate 5% "clearly elevated" bar.
   - Its cost is measured on the labels and on every submission (`guard_false_blocks`), not tuned away.
5. **Harm and usefulness together.** Fix rate and escalation recall are reported alongside harm, so "always escalate" can't top the board.
6. **A leaderboard computed from stored predictions.**
   - Entries are `bench/results/*.json` holding predictions, not numbers, so anyone can recompute every cell.
   - The page `docs/bench/index.html` is generated, and CI fails if it drifts.
   - Submissions come by pull request.
7. **Candour about synthetic data.** Each fault has one clean defining metric, so a precondition guard separates them unusually well. The page and README say the guarded columns are an upper bound.

## Consequences
- **Positive:**
  - A shareable, reproducible way to compare agents and guards on harm.
  - The first measured result: an independent guard alone took a small model from 33.9% to 0% harmful actions with no good fix blocked, and stopped every injection it had fallen for.
  - New agents are one MCP config away from a score.
- **Negative:**
  - Synthetic incidents with one author for generator and labels.
  - No Claude or GPT entries yet (needs API keys).
  - The reference guard is simpler than a real policy (single thresholds, metrics only).
  - Real incident data is the most valuable missing piece.
