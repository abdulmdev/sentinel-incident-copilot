// Uses an installed Chrome (no browser download). Override with CHROME_PATH.
import { existsSync } from "node:fs";
const candidates = [
  process.env.CHROME_PATH,
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
];
export function chromePath() {
  const found = candidates.find((p) => p && existsSync(p));
  if (!found) throw new Error("No Chrome/Chromium found; set CHROME_PATH");
  return found;
}
