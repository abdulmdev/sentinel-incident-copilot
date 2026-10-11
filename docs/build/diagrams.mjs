// Renders docs/handbook/diagrams/*.mmd -> docs/handbook/images/<name>.svg (crisp, for HTML/PDF)
// and <name>.png (2x, for Markdown viewers that don't render SVG or Mermaid).
// Usage: node diagrams.mjs [name-filter]
import { run } from "@mermaid-js/mermaid-cli";
import { readdir, mkdir, stat } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromePath } from "./chrome.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = path.resolve(here, "../handbook/diagrams");
const out = path.resolve(here, "../handbook/images");
await mkdir(out, { recursive: true });

const filter = process.argv[2] ?? "";
const puppeteerConfig = { executablePath: chromePath(), args: ["--no-sandbox"] };
const mermaidConfig = JSON.parse(await (await import("node:fs/promises")).readFile(path.join(here, "mermaid.config.json"), "utf8"));
const files = (await readdir(src)).filter((f) => f.endsWith(".mmd") && f.includes(filter)).sort();

let failed = 0;
for (const f of files) {
  const name = f.replace(/\.mmd$/, "");
  try {
    await run(path.join(src, f), path.join(out, `${name}.svg`), { puppeteerConfig, mermaidConfig, quiet: true });
    await run(path.join(src, f), path.join(out, `${name}.png`), {
      puppeteerConfig, mermaidConfig, quiet: true, parseMMDOptions: { viewport: { width: 1600, height: 1200, deviceScaleFactor: 2 }, backgroundColor: "white" },
    });
    const kb = Math.round((await stat(path.join(out, `${name}.png`))).size / 1024);
    console.log(`ok   ${name} (${kb} KB png)`);
  } catch (e) {
    failed++;
    console.log(`FAIL ${name}: ${String(e.message ?? e).split("\n").slice(0, 3).join(" | ")}`);
  }
}
console.log(`${files.length - failed}/${files.length} diagrams rendered`);
process.exit(failed ? 1 : 0);
