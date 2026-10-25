// Captures the Sentinel Guard quickstart for the handbook (docs/handbook/images/screens/):
// the approvals inbox and the guard's Grafana dashboard.
// Prerequisite, from the repository root: docker compose --profile monitoring up -d --build --wait
// (let the demo agent run a minute or two, so there is something to show).
// Usage: node guard-screenshots.mjs
import puppeteer from "puppeteer-core";
import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromePath } from "./chrome.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../handbook/images/screens");
await mkdir(out, { recursive: true });

const GUARD = process.env.GUARD_URL ?? "http://localhost:8080";
const GRAFANA = process.env.GRAFANA_URL ?? "http://localhost:3000";
const TOKEN = process.env.APPROVER_TOKEN ?? "approve-me";

const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ["--no-sandbox"] });
const page = await browser.newPage();
await page.setViewport({ width: 1360, height: 900, deviceScaleFactor: 1.5 });
page.setDefaultTimeout(60_000);

async function shot(name, { fullPage = true } = {}) {
  await new Promise((r) => setTimeout(r, 800));
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage });
  console.log(`captured ${name}`);
}

try {
  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "light" }]);
  await page.goto(`${GUARD}/`, { waitUntil: "networkidle0" });
  await page.type("#token", TOKEN);
  await page.type("#name", "alice@example.com");
  await page.click("#sign-in-form button[type=submit]");
  await page.waitForSelector("#pending .request");
  await page.click("#verify");
  await page.waitForFunction(() => document.querySelector("#audit").textContent.includes("Audit chain"));
  await shot("guard-inbox");

  await page.emulateMediaFeatures([{ name: "prefers-color-scheme", value: "dark" }]);
  await page.goto(`${GRAFANA}/d/sentinel-guard?orgId=1&from=now-30m&to=now&kiosk`, { waitUntil: "networkidle0" });
  await page.waitForFunction(() => document.body.innerText.includes("verified"));
  // Grafana loads panels as they scroll into view: scroll through once so every panel renders.
  for (let y = 0; y < 3000; y += 400) {
    await page.evaluate((top) => document.querySelector(".scrollbar-view, main")?.scrollTo?.(0, top) ?? window.scrollTo(0, top), y);
    await new Promise((r) => setTimeout(r, 300));
  }
  await page.evaluate(() => { document.querySelector(".scrollbar-view, main")?.scrollTo?.(0, 0); window.scrollTo(0, 0); });
  await page.setViewport({ width: 1360, height: 1500, deviceScaleFactor: 1.5 });
  await new Promise((r) => setTimeout(r, 2500));
  await shot("guard-grafana", { fullPage: false });
} finally {
  await browser.close();
}
