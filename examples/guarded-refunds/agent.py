"""A refund agent whose refunds need a human, using the Sentinel Guard Python SDK.

    docker run --rm -p 8080:8080 -e SENTINEL_GUARD_AGENT_TOKENS=refund-bot=agent-secret \
      -e SENTINEL_GUARD_ADMIN_TOKEN=approver-secret ghcr.io/abdulmdev/sentinel-guard:2.0.0
    SENTINEL_GUARD_TOKEN=agent-secret python agent.py order-1042 42.00

The agent asks first. The guard records the request and waits for a human (this script waits up to
two minutes); approve it from another terminal with the curl command it prints. Then the refund runs,
once: asking again for the same refund needs a new approval.
"""
import sys

from sentinel_guard import ActionBlocked, ApprovalPending, Guard

guard = Guard()   # SENTINEL_GUARD_URL (default http://localhost:8080) and SENTINEL_GUARD_TOKEN


@guard.guarded("refund", target="{order_id}", intent="customer was charged twice", wait=120)
def refund(order_id: str, amount: str) -> str:
    # The real side effect would be here (a payments API call). It only runs once the guard allowed
    # exactly this order and amount, and the single-use capability was redeemed.
    return f"refunded {amount} EUR on {order_id}"


if __name__ == "__main__":
    order_id, amount = sys.argv[1], sys.argv[2]
    decision = guard.propose("refund", order_id, {"order_id": order_id, "amount": amount}, dry_run=True)
    print(f"Asking Sentinel Guard: {decision.message}")
    first = guard.propose("refund", order_id, {"order_id": order_id, "amount": amount},
                          intent="customer was charged twice")
    if first.verdict == "pending":
        print(f"To approve:  curl -X POST {guard.url}/v1/actions/{first.request_id}/approve "
              "-H 'Authorization: Bearer approver-secret' -d '{\"approver\":\"you@example.com\"}'")
    try:
        print(refund(order_id, amount))
    except ApprovalPending as p:
        sys.exit(f"Still waiting for a human: {p.message}")
    except ActionBlocked as b:
        sys.exit(f"Not allowed: {b.message}")
