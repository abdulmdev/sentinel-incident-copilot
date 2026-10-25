// Sentinel Guard approvals inbox (single-process mode). Plain DOM, no dependencies: the page is
// served by the guard under a strict Content-Security-Policy, and everything shown comes from
// agents, so all of it is inserted as text, never as HTML.
"use strict";

const $ = (id) => document.getElementById(id);
const store = {
  get(area, key) { try { return window[area].getItem(key) || ""; } catch { return ""; } },
  set(area, key, value) { try { window[area].setItem(key, value); } catch { /* private mode */ } },
  del(area, key) { try { window[area].removeItem(key); } catch { /* private mode */ } },
};
let token = store.get("sessionStorage", "sentinel-guard-token");
let name = store.get("localStorage", "sentinel-guard-name");
let timer = null;
const busy = new Set(); // requests with a decision in flight

function el(tag, attrs = {}, text) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (text !== undefined) node.textContent = text;
  return node;
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", ...(options.headers || {}) },
    cache: "no-store",
  });
  if (res.status === 401) {
    signOut("That token was not accepted. Use the approver (admin) token.");
    throw new Error("unauthorized");
  }
  return res;
}

function ago(iso) {
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.max(s, 0)}s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return new Date(iso).toLocaleString();
}

function until(iso) {
  const s = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  if (s <= 0) return "expiring";
  if (s < 90) return `expires in ${s}s`;
  return `expires in ${Math.round(s / 60)} min`;
}

function targetText(t) {
  if (!t) return "";
  if (!t.resource) return t.name;
  const kind = t.resource.replace(/s$/, "");
  return t.namespace ? `${t.namespace}/${kind}/${t.name}` : `${kind}/${t.name}`;
}

function setStatus(text, bad = false) {
  const s = $("status");
  s.textContent = text;
  s.classList.toggle("bad", bad);
}

function card(r) {
  const node = $("request-card").content.firstElementChild.cloneNode(true);
  node.dataset.id = r.id;
  const risk = node.querySelector(".risk");
  risk.textContent = r.risk || "risk";
  risk.classList.add(r.risk || "medium");
  node.querySelector(".action").textContent = r.action;
  node.querySelector(".target").textContent = targetText(r.target);
  node.querySelector(".expires").textContent = until(r.expiresAt);
  const intent = node.querySelector(".intent");
  if (r.intent) intent.textContent = `“${r.intent}”`; else intent.remove();

  const change = node.querySelector(".change");
  const entries = Object.entries(r.change || {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) change.remove();
  for (const [k, v] of entries) { change.append(el("dt", {}, k), el("dd", {}, v)); }

  const checks = node.querySelector(".checks");
  if (!r.preconditions || r.preconditions.length === 0) checks.remove();
  for (const c of r.preconditions || []) {
    const label = { observed: "observed by the guard", not_observed: "not observed", unknown: "could not check" }[c.status] || c.status;
    const li = el("li", { class: c.status }, `${c.name}: ${label}${c.value ? ` (${c.value})` : ""}${c.error ? `: ${c.error}` : ""}`);
    checks.append(li);
  }

  node.querySelector(".meta").textContent =
    `${r.agent} asked ${ago(r.createdAt)} · request ${r.id} · policy ${String(r.policyVersion || "").slice(0, 12) || "n/a"}`;

  const form = node.querySelector(".decide");
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const verdict = e.submitter && e.submitter.value === "deny" ? "deny" : "approve";
    decide(r, verdict, form.querySelector(".note").value.trim(), form);
  });
  return node;
}

async function decide(r, verdict, note, form) {
  busy.add(r.id);
  for (const b of form.querySelectorAll("button")) b.disabled = true;
  try {
    const res = await api(`/v1/actions/${encodeURIComponent(r.id)}/${verdict}`, {
      method: "POST", body: JSON.stringify({ approver: name, note }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      setStatus(`Could not ${verdict} ${r.id}: ${body.error || res.status}`, true);
    } else {
      setStatus(verdict === "approve"
        ? `Approved ${r.action} on ${targetText(r.target)}: the agent may do exactly that, once.`
        : `Denied ${r.action} on ${targetText(r.target)}.`);
    }
  } catch (err) {
    if (err.message !== "unauthorized") setStatus(`Could not reach the guard: ${err.message}`, true);
  } finally {
    busy.delete(r.id);
    refresh();
  }
}

function render(requests) {
  const pending = requests.filter((r) => r.phase === "Pending").sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  const done = requests.filter((r) => r.phase !== "Pending")
    .sort((a, b) => (b.decidedAt || b.createdAt).localeCompare(a.decidedAt || a.createdAt)).slice(0, 15);

  // Keep cards that are still pending (so a half-typed note survives a refresh); add new ones.
  const list = $("pending");
  const want = new Set(pending.map((r) => r.id));
  for (const node of [...list.children]) {
    if (!want.has(node.dataset.id) && !busy.has(node.dataset.id)) node.remove();
  }
  const have = new Set([...list.children].map((n) => n.dataset.id));
  for (const r of pending) {
    if (have.has(r.id)) {
      list.querySelector(`[data-id="${CSS.escape(r.id)}"] .expires`).textContent = until(r.expiresAt);
    } else {
      list.append(card(r));
    }
  }
  $("pending-count").textContent = String(pending.length);
  $("pending-empty").hidden = pending.length > 0;

  const tbody = $("recent");
  tbody.replaceChildren(...done.map((r) => {
    const tr = el("tr");
    tr.append(
      el("td", {}, ago(r.executedAt || r.decidedAt || r.createdAt)),
      el("td", {}, r.agent),
      el("td", {}, r.action),
      el("td", {}, targetText(r.target)),
      el("td", { class: `outcome ${r.phase}` }, r.phase),
      el("td", {}, r.decidedBy ? (r.decidedVia ? `${r.decidedBy} via ${r.decidedVia}` : r.decidedBy) : (r.phase === "Blocked" ? "policy" : "")),
    );
    return tr;
  }));
  $("recent-empty").hidden = done.length > 0;
}

async function refresh() {
  if (!token) return;
  try {
    const res = await api("/v1/actions");
    if (!res.ok) { setStatus(`The guard answered ${res.status}.`, true); return; }
    const body = await res.json();
    render(body.requests || []);
    if ($("status").classList.contains("bad") || !$("status").textContent) setStatus(`Up to date · ${new Date().toLocaleTimeString()}`);
  } catch (err) {
    if (err.message !== "unauthorized") setStatus(`Could not reach the guard: ${err.message}`, true);
  }
}

async function verify() {
  const out = $("audit");
  out.hidden = false;
  out.className = "audit";
  out.textContent = "Verifying…";
  try {
    const res = await api("/v1/audit/verify");
    const rep = await res.json();
    out.classList.add(rep.ok ? "ok" : "bad");
    const lines = [rep.ok
      ? `Audit chain OK: ${rep.events} events across ${rep.requests} requests, head #${rep.headSeq}, every signature valid.`
      : "Audit chain FAILED verification:"];
    for (const p of rep.problems || []) lines.push(`  ✗ ${p}`);
    for (const w of rep.warnings || []) lines.push(`  ! ${w}`);
    lines.push(`Policy versions kept: ${rep.policies}.`);
    out.textContent = lines.join("\n");
  } catch (err) {
    if (err.message !== "unauthorized") out.textContent = `Could not verify: ${err.message}`;
  }
}

async function exportEvidence() {
  try {
    const res = await api("/v1/audit/export");
    if (!res.ok) { setStatus(`Export failed: ${res.status}`, true); return; }
    const disposition = res.headers.get("Content-Disposition") || "";
    const match = /filename="([^"]+)"/.exec(disposition);
    const url = URL.createObjectURL(await res.blob());
    const a = el("a", { href: url, download: match ? match[1] : "sentinel-guard-evidence.json" });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    setStatus("Evidence downloaded. Verify it offline with: kubectl sentinel audit verify --bundle <file>");
  } catch (err) {
    if (err.message !== "unauthorized") setStatus(`Export failed: ${err.message}`, true);
  }
}

function show() {
  const signedIn = Boolean(token && name);
  $("sign-in").hidden = signedIn;
  $("app").hidden = !signedIn;
  $("who").hidden = !signedIn;
  $("who-name").textContent = name;
  clearInterval(timer);
  if (signedIn) {
    refresh();
    timer = setInterval(refresh, 2000);
  } else {
    $("name").value = name;
  }
}

function signOut(message = "") {
  token = "";
  store.del("sessionStorage", "sentinel-guard-token");
  $("pending").replaceChildren();
  $("sign-in-error").textContent = message;
  show();
}

document.addEventListener("DOMContentLoaded", () => {
  $("sign-in-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    token = $("token").value.trim();
    name = $("name").value.trim();
    $("sign-in-error").textContent = "";
    try {
      const res = await api("/v1/actions");
      if (!res.ok) { $("sign-in-error").textContent = `The guard answered ${res.status}.`; return; }
    } catch (err) {
      if (err.message !== "unauthorized") $("sign-in-error").textContent = `Could not reach the guard: ${err.message}`;
      return;
    }
    store.set("sessionStorage", "sentinel-guard-token", token);
    store.set("localStorage", "sentinel-guard-name", name);
    $("token").value = "";
    show();
  });
  $("sign-out").addEventListener("click", () => signOut());
  $("verify").addEventListener("click", verify);
  $("export").addEventListener("click", exportEvidence);
  show();
});
