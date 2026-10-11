// Builds the single-file handbook from docs/handbook/*.md:
//   docs/handbook/Sentinel-Handbook.html  (self-contained: images embedded)
//   docs/Sentinel-Handbook.pdf            (A4, cover, contents, page numbers)
// Usage: node book.mjs   (run `node diagrams.mjs` first after changing a diagram)
import { marked } from "marked";
import puppeteer from "puppeteer-core";
import { readFile, writeFile, readdir } from "node:fs/promises";
import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chromePath } from "./chrome.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const hb = path.resolve(here, "../handbook");
const chapters = (await readdir(hb)).filter((f) => /^\d\d-.*\.md$/.test(f)).sort();
const status = ((await readFile(path.join(hb, "README.md"), "utf8")).match(/\*\*Status:\*\* ([^.]+\.[^.]*?\))/) ?? [])[1] ?? "";

// GitHub-style heading slugs, namespaced per chapter so anchors are unique in one document.
const slug = (text) => text.toLowerCase().replace(/<[^>]+>/g, "").replace(/[^\p{L}\p{N}\s-]/gu, "").trim().replace(/\s/g, "-");
const chapterId = (file) => file.replace(/\.md$/, "");

async function dataUri(rel) {
  const svg = rel.replace(/\.png$/, ".svg");
  const use = !rel.includes("/screens/") && existsSync(path.join(hb, svg)) ? svg : rel; // diagrams as vector
  const buf = await readFile(path.join(hb, use));
  const mime = use.endsWith(".svg") ? "image/svg+xml" : "image/png";
  return `data:${mime};base64,${buf.toString("base64")}`;
}

function rewriteLinks(md) {
  return md.replace(/\]\((?!https?:|#)([^)\s]+)\)/g, (m, target) => {
    const [file, anchor] = target.split("#");
    if (/^\d\d-[^/]+\.md$/.test(file)) return `](#${chapterId(file)}${anchor ? "--" + anchor : ""})`;
    if (file === "README.md") return "](#about)";
    return m; // ADRs, repo files: keep relative (valid next to the HTML in docs/handbook/)
  });
}

const toc = [];
const sections = [];
for (const file of chapters) {
  const id = chapterId(file);
  let md = await readFile(path.join(hb, file), "utf8");
  md = md.replace(/^(\d\d-[^#]*?)#/m, "$1#"); // no-op guard
  md = md.replace(/(?<!\]\()(?:\]\(#)([a-z0-9-]+)\)/g, `](#${id}--$1)`); // in-chapter anchors
  md = rewriteLinks(md);
  const heads = [];
  const renderer = new marked.Renderer();
  renderer.heading = function ({ tokens, depth }) {
    const text = this.parser.parseInline(tokens);
    const hid = depth === 1 ? id : `${id}--${slug(text)}`;
    if (depth === 1) toc.push({ id, title: text, subs: heads });
    if (depth === 2) heads.push({ id: hid, title: text });
    return `<h${depth} id="${hid}">${text}</h${depth}>\n`;
  };
  renderer.image = ({ href, text }) => `<figure><img src="${href}" alt="${text.replace(/"/g, "&quot;")}"></figure>`;
  let html = marked.parse(md, { renderer, gfm: true });
  // Captions: the italic "Figure X.Y" paragraph right after a figure becomes its <figcaption>.
  html = html.replace(/<\/figure>\s*<p><em>(Figure [^<]+)<\/em><\/p>/g, "<figcaption>$1</figcaption></figure>");
  html = html.replace(/<p>(<figure>.*?<\/figure>)<\/p>/gs, "$1");
  for (const [, src] of html.matchAll(/<img src="(images\/[^"]+)"/g)) {
    html = html.replace(`src="${src}"`, `src="${await dataUri(src)}"`);
  }
  sections.push(`<section class="chapter">${html}</section>`);
}

const tocHtml = toc.map((c) => `<li><a href="#${c.id}">${c.title}</a><ol>${c.subs.map((s) => `<li><a href="#${s.id}">${s.title}</a></li>`).join("")}</ol></li>`).join("");
const hero = await dataUri("images/01-capabilities.png");
const built = new Date().toISOString().slice(0, 10);

const html = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sentinel Engineering Handbook</title>
<style>
  :root { --ink:#1d1d1b; --muted:#5f5f59; --line:#dddcd6; --accent:#2f5bd3; --chip:#f1f0eb; --bg:#ffffff; }
  @media (prefers-color-scheme: dark) { :root:not([data-print]) { --ink:#ecebe6; --muted:#a3a29b; --line:#33332f; --accent:#8aa6ff; --chip:#26261f; --bg:#141413; } :root:not([data-print]) figure img { background:#fff; border-radius:6px; } }
  html { background: var(--bg); }
  body { margin:0 auto; max-width: 980px; padding: 0 28px 80px; color: var(--ink); background: var(--bg);
         font: 15px/1.6 -apple-system, "Helvetica Neue", Arial, sans-serif; }
  h1 { font-size: 30px; margin: 56px 0 8px; padding-bottom: 8px; border-bottom: 3px solid var(--accent); }
  h2 { font-size: 21px; margin: 34px 0 10px; } h3 { font-size: 17px; margin: 24px 0 8px; } h4 { font-size: 15px; }
  a { color: var(--accent); text-decoration: none; }
  table { border-collapse: collapse; width: 100%; margin: 12px 0 18px; font-size: 13.5px; }
  th, td { border: 1px solid var(--line); padding: 6px 9px; text-align: left; vertical-align: top; }
  th { background: var(--chip); font-weight: 600; }
  code { font: 12.5px/1.45 ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--chip); padding: 1px 4px; border-radius: 4px; }
  pre { background: var(--chip); padding: 12px 14px; border-radius: 8px; overflow-x: auto; }
  pre code { background: none; padding: 0; }
  blockquote { margin: 14px 0; padding: 8px 14px; border-left: 4px solid var(--accent); background: var(--chip); }
  figure { margin: 18px 0 22px; text-align: center; }
  figure img { max-width: 100%; height: auto; border: 1px solid var(--line); border-radius: 6px; }
  figcaption { font-size: 13px; color: var(--muted); margin-top: 6px; font-style: italic; }
  .cover { min-height: 92vh; display: flex; flex-direction: column; justify-content: center; }
  .cover h1 { font-size: 46px; border: none; margin: 0; }
  .cover .sub { font-size: 20px; color: var(--muted); margin: 10px 0 26px; }
  .cover .meta { color: var(--muted); font-size: 14px; }
  .cover img { max-width: 100%; margin: 26px 0; }
  nav.toc ol { padding-left: 20px; } nav.toc > ol > li { margin: 8px 0; font-weight: 600; }
  nav.toc ol ol { font-weight: 400; font-size: 13.5px; columns: 2; }
  @media print {
    body { max-width: none; padding: 0; font-size: 11.5pt; }
    .chapter { break-before: page; } h1 { margin-top: 0; }
    figure, table, pre { break-inside: avoid; } h2, h3 { break-after: avoid; }
    figure img { max-height: 225mm; object-fit: contain; }
    a { color: var(--ink); }
  }
</style></head><body>
<div class="cover">
  <h1>Sentinel</h1>
  <div class="sub">Engineering handbook: an agentic incident-response copilot</div>
  <div class="meta">High-level design · low-level design of every service · data model · APIs · security · evaluation · observability · deployment · testing · phase history<br>${status} · built ${built}</div>
  <img src="${hero}" alt="What Sentinel does">
</div>
<section class="chapter" id="about"><h1>Contents</h1><nav class="toc"><ol>${tocHtml}</ol></nav>
<p class="meta">Source of truth: the Markdown chapters in <code>docs/handbook/</code> (this file is generated by <code>docs/build/book.mjs</code>). Diagrams are rendered from <code>docs/handbook/diagrams/*.mmd</code>; screenshots come from the running system via <code>docs/build/screenshots.mjs</code>.</p></section>
${sections.join("\n")}
</body></html>`;

const htmlPath = path.join(hb, "Sentinel-Handbook.html");
await writeFile(htmlPath, html);
console.log(`html: ${htmlPath} (${Math.round(html.length / 1024)} KB, ${chapters.length} chapters)`);

const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ["--no-sandbox"] });
try {
  const page = await browser.newPage();
  await page.goto(`file://${htmlPath}`, { waitUntil: "load" });
  await page.evaluate(() => document.documentElement.setAttribute("data-print", ""));
  const pdfPath = path.resolve(hb, "../Sentinel-Handbook.pdf");
  await page.pdf({
    path: pdfPath, format: "A4", printBackground: true, outline: true, tagged: true,
    margin: { top: "16mm", bottom: "16mm", left: "15mm", right: "15mm" },
    displayHeaderFooter: true,
    headerTemplate: `<div style="font-size:7.5pt;color:#888;width:100%;padding:0 15mm;text-align:right">Sentinel engineering handbook</div>`,
    footerTemplate: `<div style="font-size:7.5pt;color:#888;width:100%;padding:0 15mm;text-align:center"><span class="pageNumber"></span> / <span class="totalPages"></span></div>`,
  });
  console.log(`pdf:  ${pdfPath}`);
} finally {
  await browser.close();
}
