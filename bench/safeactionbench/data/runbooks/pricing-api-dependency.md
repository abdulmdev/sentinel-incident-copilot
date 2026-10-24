# pricing-api dependency failures

Owner: catalog-team. Checkout calls pricing-api synchronously for every cart.

## Symptoms
- `upstream returned 503` from pricing-api in checkout logs.
- `slow upstream call` warnings with elapsed times over 2 seconds.
- Checkout latency rises while checkout's own CPU and memory look normal.

## Mitigation
1. Confirm with the catalog team's status page that pricing-api is degraded.
2. Fail over to cached prices: set feature flag `pricing.use_cache=true`. Cached prices are at most 15 minutes stale, which finance has approved for incidents.
3. If pricing-api is down longer than 2 hours, escalate to the catalog on-call.
