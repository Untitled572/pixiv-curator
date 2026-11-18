// Offline regression for preview cancellation, complete manual refresh, and bounded loading.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const { chromium } = require(process.env.PIXIV_CURATOR_PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const html = fs.readFileSync(path.join(root, "internal/admin/templates/home.html"), "utf8")
  .replace('{{define "home.html"}}', "").replace("{{end}}", "")
  .replace("{{.CSS}}", () => ["admin.css", "viewer.css"].map((f) => fs.readFileSync(path.join(root, "internal/admin/static", f), "utf8")).join("\n"))
  .replace("{{.JS}}", () => ["viewer.js", "admin.js"].map((f) => fs.readFileSync(path.join(root, "internal/admin/static", f), "utf8")).join("\n"));
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/CukAAAAASUVORK5CYII=", "base64");
let round = 1, starts = 0, decisions = 0, activePreviews = 0, maxActivePreviews = 0;
const held = [];
const previewRequests = [];
const previewAttempts = new Map();
const items = (n) => Array.from({ length: 30 }, (_, i) => ({ artist_id: String(n * 1000 + i), artist: { id: String(n * 1000 + i), name: `round ${n} artist ${i}` }, score: .5, sources: n === 2 && i === 0 ? ["directed_random"] : [], sample_illust_ids: [String(n * 10000 + i * 3), String(n * 10000 + i * 3 + 1), String(n * 10000 + i * 3 + 2)], content_rating: "safe", ai_status: "non_ai" }));
const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  res.setHeader("Cache-Control", "no-store");
  if (url.pathname === "/admin/") { res.setHeader("Content-Type", "text/html; charset=utf-8"); return res.end(html); }
  if (url.pathname.startsWith("/admin/api/previews/")) {
    res.setHeader("Content-Type", "image/png");
    const image = Number(url.pathname.split("/")[4]);
    previewRequests.push(image);
    const attempt = (previewAttempts.get(image) || 0) + 1;
    previewAttempts.set(image, attempt);
    activePreviews++; maxActivePreviews = Math.max(maxActivePreviews, activePreviews);
    res.on("close", () => { activePreviews--; });
    if (image === 20002 && attempt === 1) { res.statusCode = 503; return res.end("temporary preview failure"); }
    if (image < 20000 || image >= 30000) { held.push(res); return; }
    return res.end(png);
  }
  res.setHeader("Content-Type", "application/json");
  if ((url.pathname === "/admin/api/recommendations" || url.pathname === "/admin/api/recommendations/directed") && req.method === "POST") { round++; starts++; return res.end(JSON.stringify({ run_id: `round-${round}` })); }
  if (/\/artists\/\d+\/(approve|reject)$/.test(url.pathname)) decisions++;
  if (url.pathname === "/admin/api/recommendation-runs/latest" || url.pathname === "/admin/api/recommendations") return res.end(JSON.stringify({ run: { id: `round-${round}`, state: "succeeded", progress_percent: 100, budget: { directed: round === 2 } }, items: items(round) }));
  if (url.pathname === "/admin/api/stats") return res.end(JSON.stringify({ image_pages: 100 }));
  return res.end(JSON.stringify({ items: [] }));
});
(async () => {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE ? { executablePath: process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE } : {}) });
    const page = await browser.newPage();
    await page.setViewportSize({ width: 390, height: 844 });
    const errors = []; page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/admin/`);
    await page.locator("#tab-discover").click();
    await page.waitForFunction(() => document.querySelectorAll(".recommendation-card").length === 30 && document.querySelector("#recommendation-status .run-meta")?.textContent === "100%");
    assert.equal(await page.locator("#start-recommendations").isEnabled(), true, "completed discovery is ready before images finish");
    await page.waitForTimeout(120);
    assert.ok(previewRequests.filter((id) => id < 20000).length < 30, "offscreen cards must not trigger all 90 preview requests");
    await page.locator("#toggle-directed-discovery").click();
    await page.locator("#directed-discovery [name=tags]").fill("原創，風景");
    await page.locator("#directed-discovery [name=rating]").selectOption("safe");
    await page.locator("#directed-discovery [name=orientation]").selectOption("landscape");
    await page.locator("#start-recommendations").click();
    await page.waitForFunction(() => document.querySelectorAll(".recommendation-card").length === 30 && document.querySelector(".recommendation-card").dataset.artistId === "2000" && document.querySelector("#recommendation-status .run-meta")?.textContent === "100%");
    assert.equal(starts, 1);
    assert.equal(await page.locator("#recommendation-status .run-state").textContent(), "定向随机发现");
    assert.equal(await page.locator('.recommendation-card[data-artist-id="2000"] .score-badge').textContent(), "定向随机");
    assert.equal(decisions, 0, "manual refresh does not approve unfinished candidates");
    assert.ok((await page.locator(".recommendation-card").evaluateAll((cards) => cards.map((c) => Number(c.dataset.artistId)))).every((id) => id >= 2000 && id < 2030));
    for (const res of held.splice(0)) if (!res.destroyed) res.end(png);
    await page.waitForTimeout(150);
    assert.equal(await page.locator(".recommendation-card").first().getAttribute("data-artist-id"), "2000", "late old previews cannot replace the fresh round");
    const firstSamples = page.locator('.recommendation-card[data-artist-id="2000"] .candidate-samples');
    await firstSamples.evaluate((slider) => { slider.scrollLeft = slider.scrollWidth; });
    await page.waitForFunction(() => document.querySelector('.recommendation-card[data-artist-id="2000"] .sample-tile:nth-child(3) .preview-retry-hint'));
    await page.locator('.recommendation-card[data-artist-id="2000"] .sample-tile').nth(2).click();
    await page.waitForFunction(() => document.querySelector('.recommendation-card[data-artist-id="2000"] .sample-tile:nth-child(3) img')?.naturalWidth > 0);
    assert.equal(previewAttempts.get(20002), 2, "a failed sample can be retried independently");
    assert.equal(await page.locator('.recommendation-card[data-artist-id="2000"] [data-approve]').isEnabled(), true, "one loaded sample enables approval immediately");
    assert.ok(maxActivePreviews <= 3, "preview requests stay within the small concurrent limit");
    await page.locator("#toggle-directed-discovery").click();
    await page.locator("#tab-discover").click();
    await page.waitForFunction(() => document.querySelector("#recommendation-status .run-meta")?.textContent === "100%");
    assert.equal(await page.locator("#start-recommendations").isEnabled(), true);
    await page.locator("#start-recommendations").click();
    await page.waitForFunction(() => document.querySelector("#recommendation-status .run-meta")?.textContent === "100%" && document.querySelectorAll(".recommendation-card").length === 30 && document.querySelector(".recommendation-card")?.dataset.artistId === "3000", null, { timeout: 5000 });
    assert.equal(await page.locator("#start-recommendations").isEnabled(), true);
    assert.deepEqual(errors, []);
    console.log("PASS: immediate completed cards, viewport and horizontal progressive previews, single-image retry, stale cancellation, and bounded concurrency");
  } finally {
    for (const res of held) res.destroy();
    await browser?.close();
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });
