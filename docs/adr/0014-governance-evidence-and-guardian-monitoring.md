# ADR-0014: Governance evidence and guardian monitoring

**Status:** accepted, 2026-10-24. Builds on [ADR-0010](0010-sentinel-guard.md) (the guard and its audit chain).

## Context
The guard already kept a signed, hash-chained record, verifiable with `kubectl sentinel audit verify`. Regulation and governance frameworks ask for more, and so do the people who buy and run agents:
- the EU AI Act's record-keeping (Art. 12, 19, 26(6)), human-oversight (Art. 14) and monitoring (Art. 26(5), 72) obligations for high-risk systems;
- NIST AI RMF outcomes such as monitoring in production (MEASURE 2.4) and mechanisms to disengage systems (MANAGE 2.4).

Concretely, four gaps remained:
1. The record couldn't leave the cluster in a form a third party could check.
2. A decision couldn't be traced to the rules in force when it was made.
3. The chain head was unsigned: whoever could edit one ConfigMap could cut the newest events.
4. Nothing watched the agents' behaviour, or the record's integrity, between manual checks.

## Decisions
1. **A self-verifying evidence bundle, with no signature of its own.**
   - The bundle (`sentinel-guard-evidence/v1`) holds every request with its signed events, the signed head, every policy version and the public keys.
   - Every part is already signed by the guard. The verifier recomputes the oversight summary and cross-checks each request's phase, decision and decider against its signed events.
   - So `kubectl sentinel audit export` (the user's own RBAC) and `GET /v1/audit/export` (admin or policy `auditors`) produce equally trustworthy files. The guard's private key never leaves it.
   - Verification is offline (`--bundle`), against the bundle's key (reported by ID) or a pinned one (`--jwks`).
2. **Policy versions are part of the record.**
   - The version is the SHA-256 of the *effective* policy (what the guard enforces, including its own protected namespace and delegates added at startup), not of the file.
   - Each version is kept, signed with when it was first loaded, in a `sentinel-guard-policies` ConfigMap (or memory).
   - Every request carries its version **inside its digest**, so changing which rules a decision claims breaks verification.
   - The engine records a version before the first request that names it.
   - Requests from older guards carry none and verify with a note.
3. **Signed checkpoints, anchored by archives rather than a transparency log.**
   - Every head is signed (sequence, hash, time). Without the key, nobody can cut events and move the head back.
   - Someone with an older signed head can restore it. Archived bundles are the anchor: `--previous` checks that a newer bundle extends the older one, nothing removed or changed.
   - The `SentinelGuardAuditHeadWentBack` alert watches the same thing continuously from Prometheus's history.
   - An external timestamping or transparency log would be stronger, but adds a dependency. It's on the roadmap.
4. **Every replica verifies the chain itself**, every 5 minutes by default (`SENTINEL_GUARD_VERIFY_INTERVAL`), from its watch cache. Results become `sentinel_guard_audit_chain_ok` and a timestamp. Cost: about 0.18 s per 1,000 requests on an M1 Pro.
5. **Guardian monitoring is metrics plus shipped, tested alert rules.**
   - Per-agent outcome counters (`proposed`, `blocked`, `auto_approved`, `approved`, `denied`, `executed`, `expired`), the approval backlog and the chain's health.
   - Drift is each agent against its own 7-day baseline, as recording rules plus alerts. They ship in the chart as a PrometheusRule from one rule file that `promtool test rules` unit-tests in CI, with a firing and a quiet case per alert.
   - No ML anomaly detection: ratios against a baseline are explainable and testable.
6. **Compliance documentation maps; it doesn't claim.**
   - `docs/compliance/` maps features to AI Act articles and NIST AI RMF subcategories, paraphrasing the law and quoting NIST's public-domain text.
   - It states what remains the provider's or deployer's.
   - "Not legal advice" is said up front.
7. **A one-click demo (Codespaces), on the single-process guard.**
   - The devcontainer builds the guard, the plugin and the SDK.
   - `scripts/guard-demo.sh` tells the whole story in about a minute without Docker: ask, approve, once, replay refused, export, verify, tampering caught. Each step asserts its own result, and CI runs it.
   - The Kubernetes demo is a second, optional step (kind inside the Codespace).

## Consequences
- **Positive:**
  - Auditors, customers and regulators can verify the record themselves, from one file, without trusting the operator.
  - Each decision names its rules.
  - Integrity is checked continuously, not only when someone remembers to.
  - Agent drift is visible per agent.
  - A new user sees the guard work, end to end, in a minute.
- **Negative:**
  - Rollback to an older signed head is caught only against an archived bundle or Prometheus history, not by the bundle alone.
  - Records are never compacted, so etcd grows by about 2 KB per request: export, then prune with checkpoints, is still missing.
  - One approver per request (no *N*-of-*M*).
  - The bundle can contain personal data (targets, intents, notes): retention must be reconciled with data-protection law.
