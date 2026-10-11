// Captures real screenshots of the running system for the handbook (docs/handbook/images/screens/).
// Prerequisites: `scripts/dev-local.sh up` (optionally SENTINEL_LLM_PROVIDER=ollama), knowledge seeded,
// and a fault injected (python3 chaos-lab/inject.py checkout bad_deploy).
// Usage: node screenshots.mjs                         (every section)
//        SECTIONS=closedloop,list,grafana node screenshots.mjs   (only some)
// Sections: login, incident (LLM-driven, slow), list, knowledge, ask (needs an LLM), jaeger,
//           closedloop (needs the alert pipeline: an alert-opened incident awaiting approval), grafana, prometheus;
//           opt-in: jaegerloop (just the newest closed-loop decision trace).
// SKIP_INCIDENT=1 is kept as shorthand for "everything except login and incident".
import puppeteer from "puppeteer-core";
import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromePath } from "./chrome.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../handbook/images/screens");
await mkdir(out, { recursive: true });

const CONSOLE = process.env.CONSOLE_URL ?? "http://localhost:3000";
const JAEGER = process.env.JAEGER_URL ?? "http://localhost:16686";
const GRAFANA = process.env.GRAFANA_URL ?? "http://localhost:3001";
const PROMETHEUS = process.env.PROMETHEUS_URL ?? "http://localhost:9090";
const LLM_TIMEOUT = 600_000; // a local 8B model can take minutes per incident

const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ["--no-sandbox"] });
const page = await browser.newPage();
await page.setViewport({ width: 1360, height: 900, deviceScaleFactor: 1.5 });
page.setDefaultTimeout(60_000);

async function shot(name, { fullPage = true } = {}) {
  await new Promise((r) => setTimeout(r, 600));
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage });
  console.log(`captured ${name}`);
}

async function clickButton(text) {
  await page.waitForFunction((t) => [...document.querySelectorAll("button")].some((b) => b.textContent.trim() === t && !b.disabled), {}, text);
  await page.evaluate((t) => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === t).click(), text);
}

const ALL = ["login", "incident", "list", "knowledge", "ask", "jaeger", "closedloop", "grafana", "prometheus"];
// (extra, opt-in: "jaegerloop" = only the newest closed-loop decision trace;
//  "identity" = sign in through Keycloak (OIDC) instead of the dev login, capturing its page and Integrations)
const SECTIONS = new Set(process.env.SECTIONS ? process.env.SECTIONS.split(",")
  : process.env.SKIP_INCIDENT ? ALL.filter((s) => s !== "login" && s !== "incident") : ALL);
const want = (s) => SECTIONS.has(s);

const waitForText = (text, timeout = 60_000) =>
  page.waitForFunction((t) => document.body.innerText.includes(t), { timeout }, text);

try {
  // 1. Sign in through the BFF (dev login) and open an incident from the UI.
  // SKIP_INCIDENT=1 resumes after the (slow, LLM-driven) incident screenshots.
  await page.goto(`${CONSOLE}/login`, { waitUntil: "networkidle0" });
  if (want("login")) await shot("console-login", { fullPage: false });
  if (want("identity")) {
    // Real OIDC: Keycloak's sign-in page, then the console as an OIDC user (dev realm test user).
    const realm = JSON.parse(await readFile(path.resolve(here, "../../deploy/keycloak/sentinel-realm.json"), "utf8"));
    const alice = realm.users.find((u) => u.username === "alice@acme");
    await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }),
      page.evaluate(() => [...document.querySelectorAll("a")].find((a) => a.textContent.includes("Keycloak")).click())]);
    await page.waitForSelector("#username");
    await shot("keycloak-login", { fullPage: false });
    await page.type("#username", alice.username);
    await page.type("#password", alice.credentials[0].value);
    await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }), page.click("#kc-login")]);
    await page.waitForFunction(() => location.pathname === "/", { timeout: 30_000 });
    await page.goto(`${CONSOLE}/settings/integrations`, { waitUntil: "networkidle0" });
    await page.waitForSelector("table tbody tr");
    await shot("console-integrations", { fullPage: false });
  } else {
    await page.evaluate(() => fetch("/api/session", { method: "POST", body: JSON.stringify({ user: "alice@acme" }) }));
  }
  let incidentUrl = "(skipped)";
  if (want("incident")) {
  await page.goto(`${CONSOLE}/`, { waitUntil: "networkidle0" });
  await clickButton("Open incident");
  await page.waitForFunction(() => location.pathname.startsWith("/incidents/"), { timeout: LLM_TIMEOUT });
  await waitForText("Proposed remediation", LLM_TIMEOUT);
  incidentUrl = page.url();
  await shot("console-incident-proposed");

  // 2. Approve -> resolved with postmortem.
  await clickButton("Approve");
  await waitForText("Postmortem:", 120_000);
  await shot("console-incident-resolved");
  }

  // 2b. Closed loop: an incident Alertmanager opened, approved here, executed and verified.
  let decisionAt = 0;
  if (want("closedloop")) {
    const rows = await page.evaluate(async () => (await fetch("/api/proxy/api/v1/incidents")).json());
    const alertOpened = rows.find((r) => r.source === "alertmanager" && r.status === "AWAITING_APPROVAL");
    if (!alertOpened) throw new Error("closedloop: no alert-opened incident awaiting approval (inject a fault and wait for the alert)");
    // The alert that opened it, still firing (before we fix it).
    await page.goto(`${PROMETHEUS}/alerts?state=firing`, { waitUntil: "networkidle2" });
    await new Promise((r) => setTimeout(r, 2500));
    await shot("prometheus-alerts", { fullPage: false });
    await page.goto(`${CONSOLE}/incidents/${alertOpened.id}`, { waitUntil: "networkidle0" });
    await waitForText("Proposed remediation");
    decisionAt = Date.now();
    await clickButton("Approve");
    await waitForText("mitigated in", 180_000).catch(() => waitForText("needs a human", 1000));
    await shot("console-closed-loop");
  }

  // 3. Incident list.
  if (want("list")) {
  await page.goto(`${CONSOLE}/`, { waitUntil: "networkidle0" });
  await page.waitForSelector("table tbody tr");
  await shot("console-incidents", { fullPage: false });
  }

  // 4. Knowledge search: vector vs keyword vs hybrid side by side.
  if (want("knowledge")) {
  await page.goto(`${CONSOLE}/knowledge`, { waitUntil: "networkidle0" });
  await clickButton("Search");
  await page.waitForFunction(() => document.querySelectorAll(".panel h2").length >= 3);
  await shot("console-knowledge");
  }

  // 5. Ask: first answer from the model, then the same question served from the semantic cache.
  if (want("ask")) {
  await page.goto(`${CONSOLE}/ask`, { waitUntil: "networkidle0" });
  await clickButton("Ask");
  await waitForText("Sources", LLM_TIMEOUT);
  await shot("console-ask");
  await clickButton("Ask");
  await waitForText("cached answer", LLM_TIMEOUT);
  await shot("console-ask-cached");
  }

  // 6. Jaeger: the trace of the incident we just opened, and of the closed-loop decision.
  if (want("jaeger")) {
  const traces = await (await fetch(`${JAEGER}/api/traces?service=sentinel-gateway&operation=${encodeURIComponent("http post /api/v1/incidents")}&limit=1&lookback=1h`)).json();
  const traceId = traces.data?.[0]?.traceID;
  if (traceId) {
    await page.setViewport({ width: 1600, height: 1000, deviceScaleFactor: 1.5 });
    await page.goto(`${JAEGER}/trace/${traceId}`, { waitUntil: "networkidle0" });
    await new Promise((r) => setTimeout(r, 2500));
    await shot("jaeger-incident-trace", { fullPage: false });
  } else {
    console.log("no incident trace found in Jaeger");
  }
  }
  if ((want("closedloop") && decisionAt) || want("jaegerloop")) {
    // Spans are exported in batches: poll for the newest trace containing the remediation call.
    let decision;
    for (let i = 0; i < 15 && !decision; i++) {
      const found = await (await fetch(`${JAEGER}/api/traces?service=sentinel-agent&limit=20&lookback=1h`)).json();
      decision = (found.data ?? [])
        .filter((t) => t.spans.some((sp) => sp.operationName.startsWith("execute_remediation")))
        .filter((t) => Math.min(...t.spans.map((sp) => sp.startTime)) / 1000 >= decisionAt - 5000)
        .sort((a, b) => Math.min(...b.spans.map((sp) => sp.startTime)) - Math.min(...a.spans.map((sp) => sp.startTime)))[0];
      if (!decision) await new Promise((r) => setTimeout(r, 2000));
    }
    if (decision) {
      await page.setViewport({ width: 1600, height: 1000, deviceScaleFactor: 1.5 });
      await page.goto(`${JAEGER}/trace/${decision.traceID}`, { waitUntil: "networkidle0" });
      await new Promise((r) => setTimeout(r, 2500));
      await shot("jaeger-closed-loop-trace", { fullPage: false });
    } else {
      console.log("no closed-loop decision trace found in Jaeger");
    }
  }

  // 7. Grafana dashboard (anonymous admin in dev) and Prometheus targets.
  // Grafana lazy-loads panels only once they're in the viewport: make the viewport as tall as the
  // dashboard so every panel renders, then wait for the panel containers to fill.
  if (want("grafana")) {
  await page.setViewport({ width: 1600, height: 2150, deviceScaleFactor: 1.25 });
  await page.goto(`${GRAFANA}/d/sentinel-overview/?orgId=1&kiosk&theme=light&from=now-3h&to=now`, { waitUntil: "networkidle2" });
  await page.waitForFunction(() => document.querySelectorAll("[data-viz-panel-key], .react-grid-item canvas, .react-grid-item svg").length > 8, { timeout: 60_000 }).catch(() => {});
  await new Promise((r) => setTimeout(r, 12000));
  await shot("grafana-dashboard", { fullPage: false });
  }
  if (want("prometheus")) {
  await page.goto(`${PROMETHEUS}/targets`, { waitUntil: "networkidle2" });
  await new Promise((r) => setTimeout(r, 2500));
  await shot("prometheus-targets", { fullPage: false });
  }

  console.log(`incident: ${incidentUrl}`);
} finally {
  await browser.close();
}
