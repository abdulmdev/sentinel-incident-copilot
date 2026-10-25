"""Checks the docker compose quickstart end to end: the guard, the demo agent, the inbox, and (with
--monitoring) Prometheus's rules and Grafana's dashboard. CI runs it; so can you:

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
ADMIN = {"Authorization": f"Bearer {os.getenv('APPROVER_TOKEN', 'approve-me')}"}
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
    return httpx.get(f"{GUARD}/v1/actions", headers=ADMIN).raise_for_status().json()["requests"]


def find(action: str, phase: str, exclude: set[str] = frozenset()):
    return next((r for r in requests() if r["action"] == action and r["phase"] == phase and r["id"] not in exclude), None)


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
    check(httpx.get(f"{GUARD}/v1/actions").status_code == 401, "listing requests needs the approver token")

    print("The demo agent, behind the guard")
    pending = wait("a refund waiting for a person", lambda: find("refund", "Pending"))
    check(pending and pending["agent"] == "refund-bot" and pending["risk"] == "high",
          f"a refund waits for a person: {pending and pending['id']} ({pending and pending['change'].get('amount')})")
    check(wait("a receipt", lambda: find("send-receipt", "Executed")), "a receipt went out on policy alone, and was recorded")
    blocked = wait("a blocked erasure", lambda: find("delete-customer-data", "Blocked"))
    check(blocked, f"deleting customer data was blocked: {blocked and '; '.join(blocked.get('reasons') or [])}")

    print("A person decides")
    if pending:
        r = httpx.post(f"{GUARD}/v1/actions/{pending['id']}/approve", headers=ADMIN,
                       json={"approver": "quickstart-check", "note": "duplicate charge confirmed"})
        check(r.status_code == 200, "approved in the guard (the inbox calls the same API)")
        done = wait("the agent to issue the refund",
                    lambda: next((x for x in requests() if x["id"] == pending["id"] and x["phase"] == "Executed"), None), 30)
        check(done and done["decidedBy"] == "quickstart-check", "the agent redeemed its capability and issued the refund, once")
        other = wait("a second refund", lambda: find("refund", "Pending", {pending["id"]}))
        if other:
            r = httpx.post(f"{GUARD}/v1/actions/{other['id']}/deny", headers=ADMIN,
                           json={"approver": "quickstart-check", "note": "over the limit"})
            check(r.status_code == 200, f"a second refund denied: {other['id']}")
        else:
            check(False, "a second refund arrived to deny")
        logs = wait("the agent's log", lambda: (lambda out: out if "refused (HTTP 409)" in out and "denied by quickstart-check" in out else None)(
            subprocess.run(["docker", "compose", "logs", "--no-color", "agent"], capture_output=True, text=True).stdout), 30) or ""
        check("executed once" in logs and "refused (HTTP 409)" in logs, "the agent's log: executed once, and a reused capability refused")
        check("denied by quickstart-check: over the limit" in logs, "the agent saw the denial and its note")

    rep = httpx.get(f"{GUARD}/v1/audit/verify", headers=ADMIN).json()
    check(rep.get("ok") and rep.get("events", 0) > 0, f"audit chain OK: {rep.get('events')} events across {rep.get('requests')} requests")
    ev = httpx.get(f"{GUARD}/v1/audit/export", headers=ADMIN)
    check(ev.status_code == 200 and ev.json().get("format") == "sentinel-guard-evidence/v1", "the evidence bundle exports")

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
