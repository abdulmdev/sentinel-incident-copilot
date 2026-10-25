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
let name = store.get("sessionStorage", "sentinel-guard-name");
let role = store.get("sessionStorage", "sentinel-guard-role");
let timer = null;
const busy = new Set(); // requests with a decision in flight
let requestsById = new Map(); // the latest list, for the details view

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
    signOut("That token was not accepted. Use your own approver token (the guard's SENTINEL_GUARD_APPROVER_TOKENS; in the docker compose quickstart, alice-demo or bob-demo).");
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

  const needed = Math.max(r.requiredApprovals || 0, 1);
  const approvals = r.approvals || [];
  const mine = approvals.some((a) => a.by === name);
  const progress = node.querySelector(".approvals");
  if (needed > 1) {
    progress.textContent = approvals.length
      ? `Needs ${needed} different people · approved by ${approvals.map((a) => a.by).join(", ")} · ${needed - approvals.length} more`
      : `Needs ${needed} different people to approve`;
  } else {
    progress.remove();
  }
  node.dataset.version = String(approvals.length);

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

  const meta = node.querySelector(".meta");
  meta.textContent =
    `${r.agent} asked ${ago(r.createdAt)} · request ${r.id} · policy ${String(r.policyVersion || "").slice(0, 12) || "n/a"} · `;
  meta.append(detailsButton(r.id, "details"));

  const form = node.querySelector(".decide");
  const approve = form.querySelector("button.primary");
  if (mine) {
    approve.disabled = true;
    approve.textContent = "You approved";
  } else if (needed > 1) {
    approve.textContent = `Approve (${approvals.length + 1} of ${needed})`;
  }
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
      method: "POST", body: JSON.stringify(role === "admin" ? { approver: name, note } : { note }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      setStatus(`Could not ${verdict} ${r.id}: ${body.error || res.status}`, true);
    } else {
      const left = Math.max(body.requiredApprovals || 0, 1) - (body.approvals || []).length;
      setStatus(verdict === "deny"
        ? `Denied ${r.action} on ${targetText(r.target)}.`
        : body.phase === "Pending"
          ? `Your approval of ${r.action} on ${targetText(r.target)} is recorded. It needs ${left} more, from someone else.`
          : `Approved ${r.action} on ${targetText(r.target)}: the agent may do exactly that, once.`);
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
  const versions = new Map(pending.map((r) => [r.id, String((r.approvals || []).length)]));
  for (const node of [...list.children]) {
    const gone = !want.has(node.dataset.id) || versions.get(node.dataset.id) !== node.dataset.version;
    if (gone && !busy.has(node.dataset.id)) node.remove();
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
      cell(detailsButton(r.id, r.action)),
      el("td", {}, targetText(r.target)),
      el("td", { class: `outcome ${r.phase}` }, r.phase),
      el("td", {}, deciders(r)),
    );
    return tr;
  }));
  $("recent-empty").hidden = done.length > 0;
}

function cell(child, attrs = {}) {
  const td = el("td", attrs);
  td.append(child);
  return td;
}

function detailsButton(id, label) {
  const b = el("button", { type: "button", class: "link" }, label);
  b.addEventListener("click", () => openDetail(id));
  return b;
}

// What each audit event means, for people.
const VERBS = {
  requested: "asked to",
  blocked: "was blocked from",
  "approval-added": "gave one approval for",
  approved: "approved",
  denied: "denied",
  "capability-issued": "received the capability for",
  executed: "executed",
  readmitted: "saw a retry of",
  expired: "expired:",
};

function sentence(ev, r) {
  const what = `${r.action} on ${targetText(r.target)}`;
  if (ev.type === "approved" && String(ev.actor).startsWith("policy:")) return `policy allowed ${r.agent} to ${what}`;
  if (ev.type === "expired") return `${what} expired with ${r.decision ? "no execution" : "nobody deciding"}`;
  if (ev.type === "blocked") return `${r.agent} was blocked from ${what}`;
  if (ev.type === "executed") return `${what} ran once (${ev.actor})`;
  return `${ev.actor} ${VERBS[ev.type] || ev.type} ${what}`;
}

function tile(label, value, note = "", tone = "") {
  const t = el("div", { class: `tile ${tone}` });
  t.append(el("span", { class: "tile-label" }, label), el("strong", { class: "tile-value" }, String(value)));
  if (note) t.append(el("span", { class: "tile-note" }, note));
  return t;
}

function duration(seconds) {
  if (!seconds) return "";
  if (seconds < 90) return `${Math.round(seconds)}s`;
  if (seconds < 5400) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

function renderOverview(ov, requests) {
  const o = (ov.summary && ov.summary.outcomes) || {};
  const chain = ov.chain;
  $("tiles").replaceChildren(
    tile("Waiting for people", ov.pending, ov.pending ? `oldest ${duration(ov.oldestPendingSeconds)}` : "nothing waiting", ov.pending ? "warn" : ""),
    tile("Approved, not yet run", ov.approvedUnspent, "single-use, until they expire"),
    tile("Executed", o.executed || 0, `${o.approved || 0} approved by people · ${o.auto_approved || 0} on policy`),
    tile("Blocked by policy", o.blocked || 0, "can never be approved", (o.blocked || 0) ? "bad" : ""),
    tile("Denied by people", o.denied || 0, `${(ov.summary && ov.summary.expiredUndecided) || 0} expired with nobody deciding`),
    chain
      ? tile("Audit chain", chain.ok ? "verified" : "BROKEN", `every signature, ${chain.events} events, as of ${ago(chain.at)}`, chain.ok ? "ok" : "bad")
      : tile("Audit chain", "not checked yet", "the guard verifies it every few minutes"),
  );

  const agents = ov.agents || [];
  $("agents").replaceChildren(...agents.map((a) => {
    const n = (k) => String(a.outcomes[k] || 0);
    const tr = el("tr");
    const attempts = (a.outcomes.proposed || 0) + (a.outcomes.auto_approved || 0) + (a.outcomes.blocked || 0);
    tr.append(el("td", {}, a.agent), el("td", { class: "num" }, String(attempts)), el("td", { class: "num" }, n("proposed")),
      el("td", { class: `num${a.waiting ? " warn" : ""}` }, String(a.waiting)), el("td", { class: "num" }, n("approved")),
      el("td", { class: "num" }, n("auto_approved")), el("td", { class: "num" }, n("denied")),
      el("td", { class: "num" }, n("blocked")), el("td", { class: "num" }, n("executed")), el("td", {}, ago(a.lastSeen)));
    return tr;
  }));
  $("agents-empty").hidden = agents.length > 0;

  const events = [];
  for (const r of requests) for (const ev of r.audit || []) events.push({ ev, r });
  events.sort((a, b) => b.ev.seq - a.ev.seq);
  $("activity").replaceChildren(...events.slice(0, 30).map(({ ev, r }) => {
    const li = el("li", { class: `ev ${ev.type}` });
    li.append(el("span", { class: "when muted" }, ago(ev.at)), el("span", {}, sentence(ev, r)));
    if (ev.detail && ev.type !== "capability-issued") li.append(el("span", { class: "muted" }, ` · ${ev.detail}`));
    li.append(el("span", { class: "spacer" }), detailsButton(r.id, "details"));
    return li;
  }));
  $("activity-empty").hidden = events.length > 0;
}

function row(dl, label, value) {
  if (value === undefined || value === null || value === "") return;
  dl.append(el("dt", {}, label), el("dd", {}, String(value)));
}

function openDetail(id) {
  const r = requestsById.get(id);
  if (!r) { setStatus(`Request ${id} is not in this guard's record.`, true); return; }
  $("detail-title").textContent = `${r.action} on ${targetText(r.target)}`;
  const body = $("detail-body");
  const phase = el("p", {});
  phase.append(el("span", { class: `outcome ${r.phase}` }, r.phase), el("span", { class: "muted" }, ` · request ${r.id}`));

  const facts = el("dl", { class: "facts" });
  row(facts, "Agent", r.agent);
  row(facts, "Asked", `${new Date(r.createdAt).toLocaleString()} (${ago(r.createdAt)})`);
  row(facts, "Why", r.intent);
  row(facts, "Risk", r.risk);
  row(facts, "Autonomy", r.autonomy);
  row(facts, "Reasons", (r.reasons || []).join("; "));
  row(facts, "Needs", (r.requiredApprovals || 0) > 1 ? `${r.requiredApprovals} different people` : "");
  for (const [i, a] of (r.approvals || []).entries()) {
    row(facts, `Approval ${i + 1}`, `${a.by}${a.via ? ` via ${a.via}` : ""}, ${ago(a.at)}${a.note ? `: “${a.note}”` : ""}`);
  }
  if (r.decision) {
    row(facts, "Decision", `${r.decision} by ${r.decidedBy}${r.decidedVia ? ` via ${r.decidedVia}` : ""}${r.decidedAt ? `, ${ago(r.decidedAt)}` : ""}`);
    row(facts, "Note", r.decisionNote);
  }
  row(facts, r.phase === "Pending" || r.phase === "Approved" ? "Expires" : "", r.phase === "Pending" || r.phase === "Approved" ? until(r.expiresAt) : "");
  row(facts, "Executed", r.executedAt ? new Date(r.executedAt).toLocaleString() : "");
  row(facts, "Capability", r.capabilityJti);
  row(facts, "Policy version", r.policyVersion);

  const parts = [phase, facts];
  const entries = Object.entries(r.change || {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length) {
    const change = el("dl", { class: "change" });
    for (const [k, v] of entries) change.append(el("dt", {}, k), el("dd", {}, v));
    parts.push(el("h3", {}, "The exact change"), change);
  }
  if ((r.preconditions || []).length) {
    const ul = el("ul", { class: "checks" });
    for (const c of r.preconditions) ul.append(el("li", { class: c.status }, `${c.name}: ${c.status}${c.value ? ` (${c.value})` : ""}${c.error ? `: ${c.error}` : ""}`));
    parts.push(el("h3", {}, "What the guard observed itself"), ul);
  }
  const trail = el("table", { class: "trail" });
  const head = el("tr");
  for (const h of ["#", "Event", "When", "By", "Detail", "Hash"]) head.append(el("th", {}, h));
  trail.append(head);
  for (const ev of r.audit || []) {
    const tr = el("tr");
    tr.append(el("td", {}, String(ev.seq)), el("td", {}, ev.type), el("td", {}, new Date(ev.at).toLocaleTimeString()),
      el("td", {}, ev.actor), el("td", {}, ev.detail || ""), el("td", { class: "hash" }, String(ev.hash).slice(0, 12)));
    trail.append(tr);
  }
  const wrap = el("div", { class: "table-wrap" });
  wrap.append(trail);
  parts.push(el("h3", {}, "Signed audit trail"), wrap,
    el("p", { class: "muted" }, "Each event is signed by the guard and linked to the one before it. Verify the whole chain from History."));
  body.replaceChildren(...parts);
  if (location.hash !== `#${id}`) history.replaceState(null, "", `#${id}`);
  if (!$("detail").open) $("detail").showModal();
}

function closeDetail() {
  if ($("detail").open) $("detail").close();
  if (location.hash) history.replaceState(null, "", location.pathname);
}

function showTab(name) {
  for (const b of document.querySelectorAll(".tab")) b.setAttribute("aria-pressed", String(b.dataset.tab === name));
  for (const p of document.querySelectorAll(".tabpanel")) p.hidden = p.id !== `tab-${name}`;
  store.set("sessionStorage", "sentinel-guard-tab", name);
}

function deciders(r) {
  if (!r.decidedBy) return r.phase === "Blocked" ? "policy" : "";
  const last = r.decidedVia ? `${r.decidedBy} via ${r.decidedVia}` : r.decidedBy;
  return [...(r.approvals || []).map((a) => a.by), last].join(", ");
}

async function refresh() {
  if (!token) return;
  try {
    const [res, ovRes] = await Promise.all([api("/v1/actions"), api("/v1/overview")]);
    if (!res.ok) { setStatus(`The guard answered ${res.status}.`, true); return; }
    const body = await res.json();
    const requests = body.requests || [];
    requestsById = new Map(requests.map((r) => [r.id, r]));
    render(requests);
    if (ovRes.ok) renderOverview(await ovRes.json(), requests);
    const wanted = location.hash.slice(1);
    if (wanted && !$("detail").open && requestsById.has(wanted)) openDetail(wanted);
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
  }
}

function signOut(message = "") {
  token = "";
  store.del("sessionStorage", "sentinel-guard-token");
  store.del("sessionStorage", "sentinel-guard-role");
  $("pending").replaceChildren();
  $("sign-in-error").textContent = message;
  show();
}

document.addEventListener("DOMContentLoaded", () => {
  $("sign-in-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    token = $("token").value.trim();
    $("sign-in-error").textContent = "";
    let me;
    try {
      const res = await api("/v1/whoami");
      me = await res.json();
      if (!res.ok) { $("sign-in-error").textContent = me.error || `The guard answered ${res.status}.`; return; }
    } catch (err) {
      if (err.message !== "unauthorized") $("sign-in-error").textContent = `Could not reach the guard: ${err.message}`;
      return;
    }
    if (me.role === "approver") {
      name = me.user;
    } else if (me.role === "admin") {
      name = $("name").value.trim();
      if (!name) {
        $("name-field").hidden = false;
        $("sign-in-error").textContent = "This is the shared admin token: give your name for the audit trail.";
        return;
      }
    } else {
      $("sign-in-error").textContent = `This token belongs to ${me.role === "agent" ? "an agent" : `a ${me.role}`}; agents and services can't decide here.`;
      token = "";
      return;
    }
    role = me.role;
    store.set("sessionStorage", "sentinel-guard-token", token);
    store.set("sessionStorage", "sentinel-guard-name", name);
    store.set("sessionStorage", "sentinel-guard-role", role);
    $("token").value = "";
    show();
  });
  $("sign-out").addEventListener("click", () => signOut());
  $("verify").addEventListener("click", verify);
  $("export").addEventListener("click", exportEvidence);
  for (const b of document.querySelectorAll(".tab")) b.addEventListener("click", () => showTab(b.dataset.tab));
  $("detail-close").addEventListener("click", closeDetail);
  $("detail").addEventListener("close", () => { if (location.hash) history.replaceState(null, "", location.pathname); });
  window.addEventListener("hashchange", () => { const id = location.hash.slice(1); if (id && requestsById.has(id)) openDetail(id); });
  // A notification's link (…/inbox/#<request>) opens on that request; otherwise the last tab used.
  showTab(location.hash ? "inbox" : store.get("sessionStorage", "sentinel-guard-tab") || "overview");
  show();
});
