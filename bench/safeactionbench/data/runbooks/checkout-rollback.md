# Checkout rollback runbook

Owner: payments-platform. Use when a checkout deploy causes elevated 5xx errors or failed payments.

## Symptoms
- `chaos_requests_total{status="error"}` for checkout rises within minutes of a deploy.
- Exceptions in `PaymentClient` or `CheckoutHandler` that were not present on the previous build.
- Alert: "checkout 5xx rate above 20%".

## Decide
If error rate started within 30 minutes of a deploy and the stack traces point to changed code, roll back first and debug later. Do not attempt a forward fix during a SEV1.

## Steps
1. Find the previous good build: `deployctl history checkout --limit 5`.
2. Roll back: `deployctl rollback checkout --to <version>`. This is safe: checkout is stateless and schema changes are always backward compatible for one release.
3. Watch the error rate for 10 minutes. It should return below 0.5%.
4. Freeze checkout deploys and open a postmortem.

## Verify
Error rate below 0.5% and p95 latency under 300 ms for 10 minutes.
