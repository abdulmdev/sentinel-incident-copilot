"""Checks the docker compose quickstart end to end: the guard, the demo agent, the inbox, two-person
approvals, notifications, and (with --monitoring) Prometheus's rules and Grafana's dashboard.
CI runs it; so can you:

    TICKET_EVERY=3 docker compose --profile monitoring up -d --build --wait
    uv run --with httpx python examples/quickstart/check.py --monitoring
"""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time

import httpx

GUARD = os.getenv("GUARD_URL", "http://localhost:8080")
ALICE = {"Authorization": f"Bearer {os.getenv('ALICE_TOKEN', 'alice-demo')}"}
BOB = {"Authorization": f"Bearer {os.getenv('BOB_TOKEN', 'bob-demo')}"}
failures = 0


def check(cond: object, text: str) -> None:
    global failures
    failures += not cond
    print(f"  {'✓' if cond else '✗'} {text}", flush=True)


def wait(what: str, fn, timeout: float = 90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if (v := fn()):
                return v
        except httpx.HTTPError:
            pass
        time.sleep(1)
    print(f"  … gave up waiting for {what} after {timeout:g}s")
    return None


def requests() -> list[dict]:
    return httpx.get(f"{GUARD}/v1/actions", headers=ALICE).raise_for_status().json()["requests"]


def find(action: str, phase: str, exclude: set[str] = frozenset()):
    return next((r for r in requests() if r["action"] == action and r["phase"] == phase and r["id"] not in exclude), None)


def request(rid: str) -> dict:
    return next(r for r in requests() if r["id"] == rid)


def logs(service: str) -> str:
    return subprocess.run(["docker", "compose", "logs", "--no-color", service], capture_output=True, text=True).stdout


def decide(rid: str, verdict: str, who: dict, note: str = "") -> httpx.Response:
    return httpx.post(f"{GUARD}/v1/actions/{rid}/{verdict}", headers=who, json={"note": note})


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--monitoring", action="store_true", help="also check Prometheus and Grafana")
    args = ap.parse_args()

    print("The guard and its inbox")
    check(wait("the guard", lambda: httpx.get(f"{GUARD}/readyz").status_code == 200, 30), "the guard is ready")
    r = httpx.get(f"{GUARD}/")
    check(r.status_code == 302 and r.headers.get("location") == "/inbox/", "/ opens the approvals inbox")
    r = httpx.get(f"{GUARD}/inbox/")
    csp = r.headers.get("content-security-policy", "")
    check(r.status_code == 200 and "Sentinel Guard" in r.text and "default-src 'none'" in csp and "script-src 'self'" in csp,
          "the inbox is served under a strict Content-Security-Policy")
    check(httpx.get(f"{GUARD}/v1/actions").status_code == 401, "listing requests needs an approver's token")
    me = httpx.get(f"{GUARD}/v1/whoami", headers=ALICE).json()
    check(me == {"user": "alice@example.com", "role": "approver"}, "a personal token says who is deciding: alice@example.com")

    print("The demo agent, behind the guard")
    pending = wait("a refund waiting for a person", lambda: find("refund", "Pending"))
    check(pending and pending["agent"] == "refund-bot" and pending["risk"] == "high",
          f"a refund waits for a person: {pending and pending['id']} ({pending and pending['change'].get('amount')})")
    check(wait("a receipt", lambda: find("send-receipt", "Executed")), "a receipt went out on policy alone, and was recorded")
    blocked = wait("a blocked erasure", lambda: find("delete-customer-data", "Blocked"))
    check(blocked, f"deleting customer data was blocked: {blocked and '; '.join(blocked.get('reasons') or [])}")

    print("People decide, as themselves")
    if pending:
        r = decide(pending["id"], "approve", ALICE, "duplicate charge confirmed")
        check(r.status_code == 200 and r.json()["decidedBy"] == "alice@example.com", "Alice approves with her own token")
        done = wait("the agent to issue the refund", lambda: (x := request(pending["id"]))["phase"] == "Executed" and x, 30)
        check(done and done["decidedBy"] == "alice@example.com", "the agent redeemed its capability and issued the refund, once")
        r = httpx.post(f"{GUARD}/v1/actions/{pending['id']}/approve", headers=BOB, json={"approver": "alice@example.com"})
        check(r.status_code in (400, 409), f"nobody decides under another person's name (HTTP {r.status_code})")
        other = wait("a second refund", lambda: find("refund", "Pending", {pending["id"]}))
        if other:
            r = decide(other["id"], "deny", BOB, "over the limit")
            check(r.status_code == 200 and r.json()["decidedBy"] == "bob@example.com", f"Bob denies a second refund: {other['id']}")
        else:
            check(False, "a second refund arrived to deny")

    print("Two people for a closure (policy approvals: 2)")
    closure = wait("an account closure", lambda: find("close-account", "Pending"))
    check(closure and closure.get("requiredApprovals") == 2, f"closing an account needs two people: {closure and closure['id']}")
    if closure:
        r = decide(closure["id"], "approve", ALICE, "customer confirmed by phone")
        body = r.json()
        check(r.status_code == 200 and body["phase"] == "Pending" and [a["by"] for a in body.get("approvals", [])] == ["alice@example.com"],
              "Alice's approval is recorded; it still waits")
        r = decide(closure["id"], "approve", ALICE)
        check(r.status_code == 403, f"Alice can't count twice (HTTP {r.status_code})")
        r = decide(closure["id"], "approve", BOB)
        check(r.status_code == 200 and r.json()["phase"] == "Approved", "Bob's approval decides it")
        out = wait("the agent to close the account",
                   lambda: (lambda o: o if "approved by alice@example.com, bob@example.com, executed once" in o else None)(logs("agent")), 30) or ""
        check(out, "the agent acted once, with both approvers in its capability")

    agent_log = wait("the agent's log", lambda: (lambda o: o if "refused (HTTP 409)" in o and "denied by bob@example.com" in o else None)(
        logs("agent")), 30) or ""
    check("refused (HTTP 409)" in agent_log, "the agent's log: a reused capability refused")
    check("denied by bob@example.com: over the limit" in agent_log, "the agent saw the denial and its note")

    notices = wait("notifications", lambda: (lambda o: o if "Needs 2 different people" in o and "Approved by alice@example.com; needs 1 more" in o else None)(
        logs("notifications")), 30) or ""
    check("refund-bot asks to refund" in notices and "http://localhost:8080/inbox/" in notices,
          "a notification for each request that needs a person, linking to the inbox")
    check("Approved by alice@example.com; needs 1 more" in notices, "a notification when one of two has approved")

    rep = httpx.get(f"{GUARD}/v1/audit/verify", headers=BOB).json()
    check(rep.get("ok") and rep.get("events", 0) > 0, f"audit chain OK: {rep.get('events')} events across {rep.get('requests')} requests")
    ev = httpx.get(f"{GUARD}/v1/audit/export", headers=ALICE).json()
    check(ev.get("format") == "sentinel-guard-evidence/v1" and ev["summary"].get("multiPersonApproved", 0) >= 1,
          f"the evidence bundle exports, counting {ev['summary'].get('multiPersonApproved')} approval(s) by two people")

    if args.monitoring:
        prom = os.getenv("PROMETHEUS_URL", "http://localhost:9090")
        graf = os.getenv("GRAFANA_URL", "http://localhost:3000")
        print("Monitoring")
        groups = wait("Prometheus's rules", lambda: httpx.get(f"{prom}/api/v1/rules").json()["data"]["groups"], 60) or []
        rules = [r for g in groups for r in g["rules"]]
        alerts = sum(r["type"] == "alerting" for r in rules)
        records = sum(r["type"] == "recording" for r in rules)
        check(alerts == 8 and records == 6, f"Prometheus loaded the chart's rules: {alerts} alerts, {records} recording rules")
        series = wait("the guard's metrics in Prometheus", lambda: httpx.get(
            f"{prom}/api/v1/query", params={"query": 'sentinel_guard_agent_outcomes_total{outcome="executed"}'}).json()["data"]["result"], 60)
        check(series, "Prometheus scrapes the guard's per-agent outcomes")
        ok = wait("chain_ok", lambda: httpx.get(f"{prom}/api/v1/query", params={"query": "sentinel_guard_audit_chain_ok"}).json()["data"]["result"], 60)
        check(ok and ok[0]["value"][1] == "1", "the guard's self-verification reports the chain OK")
        dash = wait("Grafana's dashboard", lambda: httpx.get(f"{graf}/api/dashboards/uid/sentinel-guard").json().get("dashboard"), 60)
        check(dash and len(dash.get("panels", [])) >= 10, f"Grafana shows the Sentinel Guard dashboard ({dash and len(dash['panels'])} panels)")

    print()
    if failures:
        print(f"{failures} check(s) failed.")
        return 1
    print("All checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
