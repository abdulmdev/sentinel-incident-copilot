import threading
import time
import uuid

import pytest

from sentinel_guard import (ActionBlocked, ApprovalPending, AsyncGuard, CapabilityRejected, Guard, GuardUnavailable,
                            canonical_change)

from .conftest import AGENT_TOKEN


def order() -> str:
    return f"order-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def guard(guard_url):
    return Guard(guard_url, token=AGENT_TOKEN, poll_interval=0.05)


def test_pending_then_approved_then_spent_once(guard, approve):
    target = order()
    with pytest.raises(ApprovalPending) as pending:
        guard.require("refund", target, {"amount": "42.00 EUR"}, intent="charged twice")
    assert "needs human approval" in pending.value.message and pending.value.request_id
    approve(pending.value.request_id)

    cap = guard.require("refund", target, {"amount": "42.00 EUR"})
    assert cap.request_id == pending.value.request_id and cap.approved_by == "alice@example.com"
    assert cap.claims["action"] == "refund" and cap.claims["change"] == {"amount": "42.00 EUR"}
    claims = guard.spend(cap.token, executor="payments")
    assert claims["approved_by"] == "alice@example.com"
    with pytest.raises(CapabilityRejected) as again:
        guard.spend(cap.token)
    assert again.value.status == 409


def test_an_approval_covers_only_those_parameters(guard, approve):
    target = order()
    with pytest.raises(ApprovalPending) as pending:
        guard.require("refund", target, {"amount": "42.00 EUR"})
    approve(pending.value.request_id)
    with pytest.raises(ApprovalPending):  # 4200 is a new request
        guard.require("refund", target, {"amount": "4200.00 EUR"})


def test_require_waits_for_a_human(guard, approve):
    target = order()
    decision = guard.propose("restart", target)
    threading.Timer(0.3, approve, args=(decision.request_id,)).start()
    started = time.monotonic()
    cap = guard.require("restart", target, wait=10)
    assert cap.request_id == decision.request_id and time.monotonic() - started < 5


def test_a_denial_blocks(guard, approve):
    target = order()
    decision = guard.propose("delete_bucket", target)
    approve(decision.request_id, approve=False, approver="bob@example.com")
    with pytest.raises(ActionBlocked) as blocked:
        guard.require("delete_bucket", target)
    assert "bob@example.com denied" in blocked.value.message


def test_dry_run_records_nothing(guard):
    d = guard.propose("refund", order(), {"amount": "1"}, dry_run=True)
    assert d.verdict == "pending" and d.request_id is None


def test_forged_and_unauthenticated_calls(guard_url, guard):
    with pytest.raises(CapabilityRejected) as forged:
        guard.spend("eyJhbGciOiJub25lIn0.eyJqdGkiOiJ4In0.")
    assert forged.value.status == 403
    with pytest.raises(GuardUnavailable):  # wrong token: 401 is not an answer, so fail closed
        Guard(guard_url, token="wrong").propose("refund", "x")
    with pytest.raises(GuardUnavailable):
        Guard("http://127.0.0.1:9", token=AGENT_TOKEN, timeout=0.5).propose("refund", "x")


def test_guarded_decorator_binds_the_call(guard, approve):
    calls = []

    @guard.guarded("refund", target="{order_id}")
    def refund(order_id, amount):
        calls.append((order_id, amount))
        return "refunded"

    target = order()
    with pytest.raises(ApprovalPending) as pending:
        refund(target, "42.00 EUR")
    assert calls == []
    approve(pending.value.request_id)
    assert refund(target, "42.00 EUR") == "refunded"
    assert calls == [(target, "42.00 EUR")]
    with pytest.raises(ApprovalPending):  # once: the next identical call needs a new approval
        refund(target, "42.00 EUR")
    assert len(calls) == 1


async def test_async_client_and_decorator(guard_url, approve):
    guard = AsyncGuard(guard_url, token=AGENT_TOKEN, poll_interval=0.05)
    ran = []

    @guard.guarded("scale_out", target="{service}", change=lambda a: {"replicas": a["replicas"]})
    async def scale_out(service, replicas):
        ran.append(replicas)

    with pytest.raises(ApprovalPending) as pending:
        await scale_out("checkout", 4)
    approve(pending.value.request_id)
    await scale_out("checkout", 4)
    assert ran == [4]


# The same golden value is asserted by the TypeScript SDK (test/guard.test.ts): an approval obtained
# through one SDK must match a call made through the other.
GOLDEN_CHANGE = {"replicas": 4, "flags": {"zones": ["b", "a"], "fast": True}, "note": "x", "ratio": 0.5}
GOLDEN_ENCODED = {"replicas": "4", "flags": '{"fast":true,"zones":["b","a"]}', "note": "x", "ratio": "0.5"}


def test_one_canonical_change_encoding_shared_with_typescript(guard, approve):
    assert canonical_change(GOLDEN_CHANGE) == GOLDEN_ENCODED
    target = order()
    with pytest.raises(ApprovalPending) as pending:
        guard.require("resize", target, GOLDEN_CHANGE)
    approve(pending.value.request_id)
    assert guard.require("resize", target, GOLDEN_ENCODED).request_id == pending.value.request_id
