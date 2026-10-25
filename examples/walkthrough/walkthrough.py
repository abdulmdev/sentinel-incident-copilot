"""Sentinel Guard in about a minute: an agent asks, a human approves, the action runs exactly once,
and the whole record exports as evidence anyone can verify offline (and that shows tampering).

    scripts/guard-demo.sh            # builds the guard, starts it, runs this, stops it

Or by hand, against a guard started with examples/walkthrough/policy.yaml:

    SENTINEL_GUARD_URL=http://localhost:8080 AGENT_TOKEN=... APPROVER_TOKEN=... \
      KUBECTL_SENTINEL=path/to/kubectl-sentinel python walkthrough.py --out .demo
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

import httpx

from sentinel_guard import CapabilityRejected, Guard

BOLD, DIM, GREEN, RED, RESET = ("\033[1m", "\033[2m", "\033[32m", "\033[31m", "\033[0m") if sys.stdout.isatty() else ("",) * 5


def step(n: int, text: str) -> None:
    print(f"\n{BOLD}{n}. {text}{RESET}")


def say(text: str, ok: bool | None = None) -> None:
    mark = {True: f"{GREEN}✓{RESET} ", False: f"{RED}✗{RESET} ", None: "  "}[ok]
    print(f"   {mark}{text}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=".demo", help="where to write the evidence files")
    args = ap.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    url = os.getenv("SENTINEL_GUARD_URL", "http://localhost:8080")
    metrics_url = os.getenv("SENTINEL_GUARD_METRICS_URL", "http://localhost:9090/metrics")
    approver = {"Authorization": f"Bearer {os.environ['APPROVER_TOKEN']}"}
    plugin = os.getenv("KUBECTL_SENTINEL", "kubectl-sentinel")
    agent = Guard(url=url, token=os.environ["AGENT_TOKEN"])
    failures = 0

    def expect(cond: object, text: str) -> None:
        nonlocal failures
        failures += not cond
        say(text, bool(cond))

    def decide(request_id: str, verdict: str, who: str, note: str, token: dict | None = None) -> httpx.Response:
        return httpx.post(f"{url}/v1/actions/{request_id}/{verdict}", headers=token or approver,
                          json={"approver": who, "note": note})

    print(f"{BOLD}Sentinel Guard walkthrough{RESET} {DIM}(guard at {url}){RESET}")

    step(1, "A refund agent asks before it acts")
    first = agent.propose("refund", "order-1042", {"order_id": "order-1042", "amount": "42.00 EUR"},
                          intent="customer was charged twice")
    say(first.message)
    expect(first.verdict == "pending", f"waiting for a human: request {first.request_id}")

    step(2, "It tries to approve its own request")
    r = decide(first.request_id, "approve", "refund-bot", "trust me", {"Authorization": f"Bearer {os.environ['AGENT_TOKEN']}"})
    expect(r.status_code in (401, 403), f"refused (HTTP {r.status_code}): agents can't approve")

    step(3, "It tries to act anyway, with a capability it made up")
    try:
        agent.spend("eyJhbGciOiJub25lIn0.eyJyZXF1ZXN0X2lkIjoib3JkZXItMTA0MiJ9.", executor="payments")
        expect(False, "a forged capability was accepted")
    except CapabilityRejected as e:
        expect(True, f"refused (HTTP {e.status}): only the guard can sign a capability")

    step(4, "A person approves exactly that refund")
    r = decide(first.request_id, "approve", "alice@example.com", "duplicate charge confirmed in Stripe")
    expect(r.status_code == 200, f"approved by alice@example.com, valid once until {r.json().get('expiresAt', '?')[:19]}")

    step(5, "The agent gets a single-use capability; the payments service redeems it")
    cap = agent.status(first.request_id).capability
    claims = agent.spend(cap, executor="payments-service")
    expect(claims.get("request_id") == first.request_id,
           f"executed once: {claims['action']} {claims['change']['amount']} on {claims['target']}, approved by {claims['approved_by']}")

    step(6, "Someone replays the same capability")
    try:
        agent.spend(cap, executor="payments-service")
        expect(False, "the capability worked twice")
    except CapabilityRejected as e:
        expect(e.status == 409, f"refused (HTTP {e.status}): {e}")

    step(7, "A bigger refund needs its own approval, and gets denied")
    big = agent.propose("refund", "order-1042", {"order_id": "order-1042", "amount": "4200.00 EUR"},
                        intent="customer asked again")
    expect(big.verdict == "pending" and big.request_id != first.request_id, f"a new request: {big.request_id}")
    r = decide(big.request_id, "deny", "bob@example.com", "over the refund limit; escalate to finance")
    expect(r.status_code == 200 and agent.status(big.request_id).verdict == "block", "denied by bob@example.com")

    step(8, "Some actions are never allowed, and some run on policy alone")
    blocked = agent.propose("delete-customer-data", "customer-77", {"reason": "GDPR request"})
    expect(blocked.verdict == "block", f"blocked by policy: {'; '.join(blocked.reasons)}")
    receipt = agent.propose("send-receipt", "order-1042", {"email": "customer@example.com"})
    expect(receipt.verdict == "allow" and bool(receipt.capability), "a receipt runs without a human (autonomy: observe), and is recorded")

    step(9, "Export the evidence, and verify it offline")
    r = httpx.get(f"{url}/v1/audit/export", headers=approver)
    evidence = out / "evidence.json"
    evidence.write_text(json.dumps(r.json(), indent=2))
    say(f"wrote {evidence} ({evidence.stat().st_size // 1024} KB): every request, its signed events, the policy version, the public key")
    run = subprocess.run([plugin, "audit", "verify", "--bundle", str(evidence)], capture_output=True, text=True)
    print("\n".join("     " + line for line in run.stdout.strip().splitlines()))
    expect(run.returncode == 0 and "Audit chain OK" in run.stdout, "the evidence verifies, with nothing but the file")

    step(10, "Someone edits the record: who approved the refund")
    bundle = json.loads(evidence.read_text())
    for req in bundle["requests"]:
        if req["id"] == first.request_id:
            req["decidedBy"] = "mallory@example.com"
    tampered = out / "evidence-tampered.json"
    tampered.write_text(json.dumps(bundle, indent=2))
    run = subprocess.run([plugin, "audit", "verify", "--bundle", str(tampered)], capture_output=True, text=True)
    print("\n".join("     " + line for line in run.stdout.strip().splitlines()[:4]))
    expect(run.returncode != 0 and "mallory" in run.stdout, "verification fails, and names the record")

    step(11, "Guardian metrics, for alerts on agent drift")
    try:
        lines = [x for x in httpx.get(metrics_url).text.splitlines()
                 if x.startswith(("sentinel_guard_agent_outcomes_total", "sentinel_guard_audit_chain_ok",
                                  "sentinel_guard_requests_open"))]
        print("\n".join("     " + line for line in lines))
        expect(any("sentinel_guard_agent_outcomes_total" in x for x in lines), "per-agent outcomes, open requests and the chain's health")
    except httpx.HTTPError as e:
        say(f"metrics not reachable at {metrics_url}: {e}")

    print()
    if failures:
        print(f"{RED}{failures} step(s) did not behave as described.{RESET}")
        return 1
    print(f"{GREEN}Every step behaved as described.{RESET} Next: the Kubernetes demo, `scripts/guard-demo.sh kubernetes`.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
