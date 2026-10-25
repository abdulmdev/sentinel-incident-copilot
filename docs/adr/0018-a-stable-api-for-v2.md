# ADR-0018: A stable API for v2.0.0

**Status:** accepted, 2026-10-25. Builds on [ADR-0010](0010-sentinel-guard.md) (the ActionRequest CRD).

## Context
The alphas served ActionRequests as `sentinel.dev/v1alpha1`, which promises nothing. A final release has to tell users what will keep working. Records written by the alphas (they are the audit trail, never deleted) have to stay readable and verifiable after an upgrade. And `helm upgrade` never updates a chart's CRDs, so a plain upgrade leaves the old CRD in place.

## Decisions
1. **`sentinel.dev/v1` is the stored, stable version. `v1alpha1` stays served, marked deprecated.** The two schemas are identical, so no conversion webhook is needed. Objects move to v1 storage when they are next written; reading them through either version works throughout.
2. **The guard checks at startup that the CRD serves `v1`.** If not, it stops with the exact fix: apply the chart's CRD file, then restart. That is better than failing on every request.
3. **Upgrades are tested, not described.** `scripts/guard-upgrade-e2e.sh`, run in CI, does the following on a real API server:
   - installs the last published release from GHCR and records an executed and a pending request with it;
   - upgrades the chart alone, and checks that the new guard says what to apply;
   - applies the CRD;
   - checks that the old records are read through v1 and verify, that `v1alpha1` is still served, and that the request left pending can be approved and is admitted once.
4. **Stability promise for v2:**
   - **Additive only:** the v1 API, the action API and the evidence format (`sentinel-guard-evidence/v1`) only gain optional fields within v2.
   - **Records:** policy and chain records written by any v2 release keep verifying.

## Consequences
- `v1alpha1` can be dropped in v3, after a storage migration (all objects rewritten as v1).
- The kubectl plugin from 2.0.0 speaks v1, so it needs the new CRD. Older plugins keep working through `v1alpha1`.
