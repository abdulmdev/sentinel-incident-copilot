# Postmortem: checkout NullPointerException after async payment client (2026-08-14)

Severity: SEV1. Duration: 47 minutes. Impact: 38% of checkout attempts failed.

## Summary
Release 1.3.0 switched `PaymentClient` to an async connection pool. Under load, the client returned before the pool initialised, causing `NullPointerException in PaymentClient.charge`.

## Timeline
- 14:02 deploy of 1.3.0 completes.
- 14:05 alert: checkout 5xx rate above 20%.
- 14:31 on-call attempted a forward fix, which failed.
- 14:49 rollback to 1.2.9 restored service.

## What went wrong
We tried a forward fix before rolling back. Rolling back at 14:06 would have cut impact by 40 minutes.

## Action items
- Roll back first for deploy-correlated incidents (now in the checkout rollback runbook).
- Add a readiness check that fails until the payment pool is initialised.
