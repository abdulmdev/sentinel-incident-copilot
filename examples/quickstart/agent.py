"""A support-desk agent that works through a queue of tickets, behind Sentinel Guard.

It runs in `docker compose up` (see compose.yaml at the repository root) and never stops on its own:
- receipts go out on policy alone (autonomy: observe), and are still recorded;
- refunds wait for a person: approve or deny them in the inbox at http://localhost:8080;
- closing an account waits for two different people (policy approvals: 2);
- deleting customer data is never allowed for an agent, so it hands those tickets to a person.

Once a refund or a closure is approved, the agent redeems its single-use capability and acts, exactly once.
Every few approvals it also tries to reuse a spent capability, to show that the guard refuses.

    SENTINEL_GUARD_URL=http://localhost:8080 SENTINEL_GUARD_TOKEN=... python agent.py
"""
from __future__ import annotations

import os
import random
import sys
import time
from dataclasses import dataclass

from sentinel_guard import CapabilityRejected, Guard, GuardUnavailable

EVERY = float(os.getenv("TICKET_EVERY", "20"))      # seconds between new tickets
MAX_WAITING = int(os.getenv("MAX_WAITING", "3"))    # refunds waiting for a person at once
INBOX = os.getenv("INBOX_URL", "http://localhost:8080")
CUSTOMERS = ["Ada", "Grace", "Linus", "Margaret", "Ken", "Barbara", "Dennis", "Frances", "Edsger", "Radia"]


def log(text: str) -> None:
    print(f"{time.strftime('%H:%M:%S')}  {text}", flush=True)


@dataclass
class Ticket:
    number: int
    kind: str          # refund | receipt | erasure | closure
    customer: str
    order: str
    amount: str = ""


def tickets(rng: random.Random):
    n = 1040
    # The first four show each path at once; after that, a realistic mix.
    for kind in ["refund", "receipt", "erasure", "closure"]:
        n += 1
        yield make(rng, n, kind)
    while True:
        n += 1
        yield make(rng, n, rng.choices(["refund", "receipt", "erasure", "closure"], weights=[5, 4, 1, 1])[0])


def make(rng: random.Random, n: int, kind: str) -> Ticket:
    amount = f"{rng.choice([12, 19, 24, 35, 42, 58, 75, 99, 140, 260])}.00 EUR"
    return Ticket(n, kind, rng.choice(CUSTOMERS), f"order-{n}", amount)


class Desk:
    def __init__(self, guard: Guard):
        self.guard = guard
        self.waiting: dict[str, Ticket] = {}   # request id -> ticket
        self.backlog: list[Ticket] = []
        self.spent: list[str] = []             # capabilities already redeemed (for the replay check)
        self.approved = 0

    def handle(self, t: Ticket) -> None:
        if t.kind == "receipt":
            d = self.guard.propose("send-receipt", t.order, {"order_id": t.order, "customer": t.customer},
                                   intent=f"ticket #{t.number}: {t.customer} asked for a receipt")
            if d.verdict == "allow":
                self.guard.spend(d.capability, executor="mailer")
                log(f"#{t.number} receipt for {t.order} sent to {t.customer} (allowed by policy, recorded)")
            else:
                log(f"#{t.number} receipt for {t.order}: {d.verdict}: {d.message}")
        elif t.kind == "erasure":
            d = self.guard.propose("delete-customer-data", t.customer.lower(), {"reason": "erasure request"},
                                   intent=f"ticket #{t.number}: {t.customer} asked us to delete their data")
            log(f"#{t.number} delete {t.customer}'s data: {d.verdict}. {'; '.join(d.reasons) or d.message}")
            log(f"#{t.number} handed to the privacy team instead")
        else:
            if len(self.waiting) >= MAX_WAITING:
                self.backlog.append(t)
                log(f"#{t.number} {t.kind} for {t.order} queued: {len(self.waiting)} already wait for people")
                return
            self.ask(t)

    def ask(self, t: Ticket) -> None:
        if t.kind == "closure":
            what = f"close {t.customer}'s account"
            d = self.guard.propose("close-account", t.customer.lower(), {"customer": t.customer.lower(), "reason": "customer request"},
                                   intent=f"ticket #{t.number}: {t.customer} asked us to close their account")
        else:
            what = f"refund {t.amount} on {t.order}"
            d = self.guard.propose("refund", t.order, {"order_id": t.order, "amount": t.amount},
                                   intent=f"ticket #{t.number}: {t.customer} was charged twice")
        if d.verdict == "pending":
            self.waiting[d.request_id] = t
            people = "two different people" if (d.request.get("requiredApprovals") or 1) > 1 else "a person"
            log(f"#{t.number} {what}: waiting for {people}, approve or deny at {INBOX}")
        elif d.verdict == "allow":
            self.issue(t, d.capability)
        else:
            log(f"#{t.number} {what}: {d.verdict}: {d.message}")

    def issue(self, t: Ticket, capability: str) -> None:
        executor = "accounts" if t.kind == "closure" else "payments"
        claims = self.guard.spend(capability, executor=executor)
        self.spent.append(capability)
        self.approved += 1
        approvers = ", ".join(claims.get("approvers") or [claims.get("approved_by", "?")])
        if t.kind == "closure":
            log(f"#{t.number} closed {t.customer}'s account, approved by {approvers}, executed once")
        else:
            log(f"#{t.number} refunded {claims['change']['amount']} on {claims['target']}, approved by {approvers}, executed once")
        if self.approved % 3 == 1:   # now and then, show that a capability works only once
            try:
                self.guard.spend(capability, executor="payments")
                log(f"#{t.number} !! a spent capability worked twice")
            except CapabilityRejected as e:
                log(f"#{t.number} reusing that capability is refused (HTTP {e.status}): one approval, one execution")

    def poll(self) -> None:
        for request_id, t in list(self.waiting.items()):
            try:
                d = self.guard.status(request_id)
            except GuardUnavailable as e:
                if "HTTP 404" not in str(e):
                    raise
                # In memory mode the guard's state lives in its process: a restarted guard has
                # forgotten the request (and its approval), so ask again.
                del self.waiting[request_id]
                log(f"#{t.number} the guard no longer knows request {request_id} (it restarted); asking again")
                self.backlog.insert(0, t)
                continue
            if d.verdict == "pending":
                continue
            del self.waiting[request_id]
            r = d.request
            if d.verdict == "allow" and d.capability:
                self.issue(t, d.capability)
            elif r.get("phase") == "Denied":
                note = f": {r['decisionNote']}" if r.get("decisionNote") else ""
                log(f"#{t.number} {t.kind} for {t.order} denied by {r.get('decidedBy')}{note}. Leaving it to a person")
            else:
                log(f"#{t.number} {t.kind} for {t.order}: {r.get('phase', d.verdict)}. {d.message}")
        while self.backlog and len(self.waiting) < MAX_WAITING:
            self.ask(self.backlog.pop(0))


def main() -> int:
    guard = Guard()   # SENTINEL_GUARD_URL and SENTINEL_GUARD_TOKEN
    desk = Desk(guard)
    rng = random.Random(int(os.getenv("SEED", "7")))
    log(f"support-desk agent started: guard at {guard.url}, a new ticket every {EVERY:g}s")
    queue = tickets(rng)
    handled, next_ticket = 0, time.monotonic()
    while True:
        try:
            if time.monotonic() >= next_ticket:
                desk.handle(next(queue))
                handled += 1
                next_ticket = time.monotonic() + (2 if handled < 4 else EVERY)   # the first four at once
            desk.poll()
        except GuardUnavailable as e:
            log(f"the guard is unreachable, so nothing is done: {e}")
            time.sleep(5)
        time.sleep(1)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(0)
