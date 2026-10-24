// Runs against the real Sentinel Guard (built from ../../guard, memory mode), not a mock.
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { ActionBlocked, ApprovalPending, CapabilityRejected, Guard, GuardUnavailable, canonicalChange } from "../src/index.ts";

const ROOT = resolve(import.meta.dirname, "../../..");
const AGENT = "ts-agent-token";
const ADMIN = "ts-admin-token";
let proc: ChildProcess;
let url: string;

const freePort = () => new Promise<number>((res) => {
  const s = createServer();
  s.listen(0, "127.0.0.1", () => { const p = (s.address() as { port: number }).port; s.close(() => res(p)); });
});

before(async () => {
  let bin = process.env.SENTINEL_GUARD_BIN;
  if (!bin) {
    const local = join(ROOT, ".tools/go/bin/go");
    bin = join(mkdtempSync(join(tmpdir(), "guard-")), "sentinel-guard");
    execFileSync(existsSync(local) ? local : "go", ["build", "-o", bin, "./cmd/sentinel-guard"],
      { cwd: join(ROOT, "guard"), env: { ...process.env, GOFLAGS: "-modcacherw" } });
  }
  const [port, metrics] = [await freePort(), await freePort()];
  url = `http://127.0.0.1:${port}`;
  proc = spawn(bin, [], { stdio: "ignore", env: { ...process.env, SENTINEL_GUARD_MODE: "memory",
    SENTINEL_GUARD_ADDR: `127.0.0.1:${port}`, SENTINEL_GUARD_METRICS_ADDR: `127.0.0.1:${metrics}`,
    SENTINEL_GUARD_AGENT_TOKENS: `ts-agent=${AGENT}`, SENTINEL_GUARD_ADMIN_TOKEN: ADMIN } });
  for (let i = 0; i < 100; i++) {
    if (await fetch(`${url}/healthz`).then((r) => r.ok, () => false)) return;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error("the guard did not start");
});
after(() => { proc?.kill(); });

const approve = async (id: string, yes = true, approver = "alice@example.com") => {
  const r = await fetch(`${url}/v1/actions/${id}/${yes ? "approve" : "deny"}`, { method: "POST",
    headers: { authorization: `Bearer ${ADMIN}`, "content-type": "application/json" }, body: JSON.stringify({ approver }) });
  assert.equal(r.status, 200);
};
const order = () => `order-${Math.random().toString(16).slice(2, 10)}`;

test("pending, approved, then spent exactly once", async () => {
  const guard = new Guard({ url, token: AGENT });
  const target = order();
  const pending = await guard.require("refund", target, { change: { amount: "42.00 EUR" } }).catch((e) => e);
  assert.ok(pending instanceof ApprovalPending && pending.message.includes("needs human approval"));
  await approve(pending.requestId);
  const cap = await guard.require("refund", target, { change: { amount: "42.00 EUR" } });
  assert.equal(cap.approvedBy, "alice@example.com");
  const claims = await guard.spend(cap.token, "payments");
  assert.equal(claims.action, "refund");
  const again = await guard.spend(cap.token).catch((e) => e);
  assert.ok(again instanceof CapabilityRejected && again.status === 409);
});

test("the approval covers only those parameters, and a denial blocks", async () => {
  const guard = new Guard({ url, token: AGENT });
  const target = order();
  const p = (await guard.require("refund", target, { change: { amount: "42.00 EUR" } }).catch((e) => e)) as ApprovalPending;
  await approve(p.requestId);
  await assert.rejects(guard.require("refund", target, { change: { amount: "4200.00 EUR" } }), ApprovalPending);
  const d = await guard.propose("delete_bucket", target);
  await approve(d.request.id!, false, "bob@example.com");
  await assert.rejects(guard.require("delete_bucket", target), (e: unknown) => e instanceof ActionBlocked && /bob@example.com denied/.test(e.message));
});

test("require waits for a human", async () => {
  const guard = new Guard({ url, token: AGENT, pollMs: 50 });
  const target = order();
  const d = await guard.propose("restart", target);
  setTimeout(() => void approve(d.request.id!), 300);
  const cap = await guard.require("restart", target, { wait: 10 });
  assert.equal(cap.requestId, d.request.id);
});

test("guarded functions run once, after approval", async () => {
  const guard = new Guard({ url, token: AGENT });
  const ran: string[] = [];
  const refund = guard.guarded("refund", { target: (id: string) => id, change: (_id: string, amount: string) => ({ amount }) },
    async (id: string, amount: string) => { ran.push(`${id}:${amount}`); return "refunded"; });
  const target = order();
  const p = (await refund(target, "9.99").catch((e) => e)) as ApprovalPending;
  assert.ok(p instanceof ApprovalPending && ran.length === 0);
  await approve(p.requestId);
  assert.equal(await refund(target, "9.99"), "refunded");
  await assert.rejects(refund(target, "9.99"), ApprovalPending);
  assert.deepEqual(ran, [`${target}:9.99`]);
});

test("fails closed: wrong token, unreachable guard, forged capability", async () => {
  await assert.rejects(new Guard({ url, token: "wrong" }).propose("refund", "x"), GuardUnavailable);
  await assert.rejects(new Guard({ url: "http://127.0.0.1:9", token: AGENT, timeoutMs: 500 }).propose("refund", "x"), GuardUnavailable);
  await assert.rejects(new Guard({ url }).spend("eyJhbGciOiJub25lIn0.eyJqdGkiOiJ4In0."), (e: unknown) => e instanceof CapabilityRejected && e.status === 403);
});

// The same golden value is asserted by the Python SDK (tests/test_client.py): an approval obtained
// through one SDK must match a call made through the other.
const GOLDEN_CHANGE = { replicas: 4, flags: { zones: ["b", "a"], fast: true }, note: "x", ratio: 0.5 };
const GOLDEN_ENCODED = { replicas: "4", flags: '{"fast":true,"zones":["b","a"]}', note: "x", ratio: "0.5" };

test("one canonical change encoding, shared with the Python SDK", async () => {
  assert.deepEqual(canonicalChange(GOLDEN_CHANGE), GOLDEN_ENCODED);
  const guard = new Guard({ url, token: AGENT });
  const target = order();
  const p = (await guard.require("resize", target, { change: GOLDEN_CHANGE }).catch((e) => e)) as ApprovalPending;
  await approve(p.requestId);
  // A caller sending the already-encoded form (which is what the Python SDK sends) gets the same approval.
  const cap = await guard.require("resize", target, { change: GOLDEN_ENCODED });
  assert.equal(cap.requestId, p.requestId);
});
