// Records the closed loop as captioned frames for the README demo (docs/media/closed-loop.gif):
// sign in (OIDC) → inject a bad deploy → the alert opens an incident → review → approve → verified.
// Prerequisites: `scripts/dev-local.sh up` (loadgen + alerting running). Then: node record-demo.mjs
// and `uv run --with pillow python make-gif.py` to assemble the GIF.
import puppeteer from "puppeteer-core";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromePath } from "./chrome.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "frames");
await rm(out, { recursive: true, force: true });
await mkdir(out, { recursive: true });

const CONSOLE = process.env.CONSOLE_URL ?? "http://localhost:3000";
const CHECKOUT = process.env.CHECKOUT_URL ?? "http://localhost:8101";
const frames = [];
let n = 0;

const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ["--no-sandbox"] });
const page = await browser.newPage();
await page.setViewport({ width: 1280, height: 800, deviceScaleFactor: 1 });
page.setDefaultTimeout(60_000);

async function frame(caption, seconds = 2.2, step = "") {
  await new Promise((r) => setTimeout(r, 400));
  const file = `${String(++n).padStart(3, "0")}.png`;
  await page.screenshot({ path: path.join(out, file) });
  frames.push({ file, caption, seconds, step });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

try {
  // 1. Sign in through Keycloak (dev realm test user, read from the realm file like the docs screenshots).
  const realm = JSON.parse(await readFile(path.resolve(here, "../../deploy/keycloak/sentinel-realm.json"), "utf8"));
  const alice = realm.users.find((u) => u.username === "alice@acme");
  await page.goto(`${CONSOLE}/api/auth/logout`, { waitUntil: "networkidle0" });
  await page.goto(`${CONSOLE}/login`, { waitUntil: "networkidle0" });
  await frame("Sign in with your company identity provider (OIDC)", 2.0, "1");
  await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }),
    page.evaluate(() => [...document.querySelectorAll("a")].find((a) => a.textContent.includes("Keycloak")).click())]);
  await page.waitForSelector("#username");
  await page.type("#username", alice.username);
  await page.type("#password", alice.credentials[0].value);
  await frame("Keycloak: code flow + PKCE; tokens never reach the browser", 1.8, "1");
  await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }), page.click("#kc-login")]);
  await page.waitForFunction(() => location.pathname === "/");

  // 2. Break production: a bad deploy on checkout (loadgen keeps traffic flowing).
  const seen = new Set((await page.evaluate(async () => (await fetch("/api/proxy/api/v1/incidents")).json())).map((r) => r.id));
  await fetch(`${CHECKOUT}/chaos/fault`, { method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type: "bad_deploy" }) });
  const injectedAt = Date.now();
  await page.goto(`${CONSOLE}/`, { waitUntil: "networkidle0" });
  await frame("A bad deploy breaks checkout: 40% of requests start failing", 2.4, "2");

  // 3. Wait for Prometheus → Alertmanager → Sentinel to open the incident by itself.
  let incident = null;
  for (let i = 0; i < 90 && !incident; i++) {
    await sleep(2000);
    const rows = await page.evaluate(async () => (await fetch("/api/proxy/api/v1/incidents")).json());
    incident = rows.find((r) => !seen.has(r.id) && r.source === "alertmanager" && r.status === "AWAITING_APPROVAL");
    if (!incident && i % 6 === 5) {
      await page.reload({ waitUntil: "networkidle0" });
      await frame(`Waiting for the alert… (${Math.round((Date.now() - injectedAt) / 1000)} s)`, 0.9, "2");
    }
  }
  if (!incident) throw new Error("no alert-opened incident appeared");
  await page.reload({ waitUntil: "networkidle0" });
  await frame(`The alert opened an incident by itself after ${Math.round((Date.now() - injectedAt) / 1000)} s`, 2.6, "3");

  // 4. Review the evidence-backed proposal.
  await page.goto(`${CONSOLE}/incidents/${incident.id}`, { waitUntil: "networkidle0" });
  await frame("The agent investigated with tools and proposes one fix, citing its evidence", 3.2, "4");
  await page.evaluate(() => window.scrollBy(0, 420));
  await frame("Every hypothesis cites the deploy record, the logs and the team's postmortem", 2.6, "4");
  await page.evaluate(() => window.scrollTo(0, 0));

  // 5. Approve: a single-use capability for exactly this action executes it, then recovery is verified.
  await page.evaluate(() => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Approve").click());
  await frame("Human approves → single-use capability → the adapter executes exactly this rollback", 1.8, "5");
  await page.waitForFunction(() => document.body.innerText.includes("mitigated in"), { timeout: 180_000 });
  const ttm = await page.evaluate(() => (document.body.innerText.match(/mitigated in (\S+)/) || [])[1]);
  await page.evaluate(() => {
    const h = [...document.querySelectorAll("h2")].find((e) => e.textContent.includes("Closed loop"));
    if (h) window.scrollTo(0, h.getBoundingClientRect().top + window.scrollY - 90);
  });
  await frame(`Verified healthy with real traffic: mitigated in ${ttm}, postmortem written`, 4.0, "6");

  await writeFile(path.join(out, "frames.json"), JSON.stringify({ frames, ttm, incident: incident.id }, null, 1));
  console.log(`recorded ${frames.length} frames; incident ${incident.id}; mitigated in ${ttm}`);
} finally {
  await browser.close();
}
