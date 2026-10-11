## What and why

## Verification
- [ ] Tests pass for the services touched (`mvn verify` / `uv run pytest` / `npm run build`)
- [ ] Behavior changes: `evals/rca/run.py --check` passes (main + held-out); thresholds/history updated if a metric moved on purpose

## Documentation (same PR, see docs/handbook/README.md "Maintaining this handbook")
- [ ] Affected handbook chapters updated (APIs → 09, schema → 08, config → 13, agent behavior → 04 + 11, metrics → 12, tests/CI → 14)
- [ ] `docs/handbook/15-phase-history.md` entry for notable changes (incl. bugs found)
- [ ] New gaps in `16-limitations-and-roadmap.md`; ADR for significant decisions
- [ ] `python3 scripts/check_docs.py` passes
