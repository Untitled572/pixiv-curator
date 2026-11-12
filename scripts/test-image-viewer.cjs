// Offline regression for the lazy management image viewer. Requires Playwright; never contacts Pixiv.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const { chromium } = require(process.env.PIXIV_CURATOR_PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const viewerJS = fs.readFileSync(path.join(root, "internal/admin/static/viewer.js"), "utf8");
const viewerCSS = fs.readFileSync(path.join(root, "internal/admin/static/viewer.css"), "utf8");
const html = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${viewerCSS}</style><body><dialog id="artwork-dialog"><h2>画师详情</h2></dialog><script>${viewerJS}</script></body></html>`;
const requests = [];
const pageSVG = (width, height) => `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}"><rect width="100%" height="100%" fill="#9ab"/></svg>`;
let slowStarted;
const slowStartedPromise = new Promise((resolve) => { slowStarted = resolve; });
const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  requests.push(url.pathname + url.search);
  if (url.pathname === "/") {
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    return res.end(html);
  }
  const match = url.pathname.match(/^\/admin\/api\/previews\/(\d+)\/(\d+)$/);
  if (match) {
    const [, id, pageIndex] = match;
    res.setHeader("Content-Type", "image/svg+xml");
    if (id === "404") { res.statusCode = 404; return res.end("not found"); }
    if (id === "555") {
      slowStarted();
      return setTimeout(() => { if (!res.destroyed) res.end(pageSVG(555, 555)); }, 1200);
    }
    if (id === "123" && pageIndex === "1") return res.end(pageSVG(800, 600));
    if (id === "777") return res.end(pageSVG(777, 555));
    return res.end(pageSVG(640, 480));
  }
  res.statusCode = 404;
  res.end("not found");
});

(async () => {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE ? { executablePath: process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE } : {}) });
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    const failedRequests = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("requestfailed", (request) => failedRequests.push(request.url()));
    await page.route("**/*", (route) => new URL(route.request().url()).hostname === "127.0.0.1" ? route.continue() : route.abort());
    await page.goto("http://127.0.0.1:" + server.address().port + "/");
    assert.equal(await page.locator("img").count(), 0, "viewer must not load an image before a click");
    assert.equal(await page.locator("dialog.pixiv-viewer-dialog").count(), 0, "dialog must be lazy-created");
    assert.equal(requests.filter((entry) => entry.startsWith("/admin/api/previews/")).length, 0);

    await page.locator("#artwork-dialog").evaluate((dialog) => dialog.showModal());
    const hostileTitle = `title <img id="injected" src="/should-not-load">`;
    assert.equal(await page.evaluate((title) => window.PixivCuratorViewer.open({ id: "123", pageIndex: 0, title, artistName: "画师 <svg>", pageCount: 2, rating: "r18", aiStatus: "ai", tags: ["风景", "<script>"] }), hostileTitle), true);
    await page.locator(".pixiv-viewer-image").waitFor();
    await page.waitForFunction(() => document.querySelector(".pixiv-viewer-image").naturalWidth === 640);
    assert.equal(await page.locator(".pixiv-viewer-title").textContent(), hostileTitle);
    assert.equal(await page.locator("#injected").count(), 0, "title must be rendered as text");
    assert.equal(await page.locator(".pixiv-viewer-image").count(), 1, "viewer should use one image element");
    assert.equal(await page.locator(".pixiv-viewer-dimensions").textContent(), "640 × 480");
    assert.ok(requests.includes("/admin/api/previews/123/0?size=original"));
    assert.equal(await page.locator(".pixiv-viewer-url").inputValue(), `http://127.0.0.1:${server.address().port}/admin/api/previews/123/0?size=original`);
    assert.equal(await page.locator(".pixiv-viewer-url").evaluate((input) => input.readOnly), true);
    assert.equal(await page.locator(".pixiv-viewer-link").first().getAttribute("href"), "https://www.pixiv.net/artworks/123");
    assert.equal(await page.locator(".pixiv-viewer-link").nth(1).getAttribute("href"), "/admin/api/previews/123/0?size=original");
    assert.equal(await page.locator(".pixiv-viewer-artist").textContent(), "画师 <svg>");
    assert.equal(await page.locator(".pixiv-viewer-tag").nth(1).textContent(), "<script>");


    await page.locator(".pixiv-viewer-page-button").last().click();
    await page.waitForFunction(() => document.querySelector(".pixiv-viewer-image").naturalWidth === 800);
    assert.ok(requests.includes("/admin/api/previews/123/1?size=original"));
    assert.equal(await page.locator(".pixiv-viewer-page-status").textContent(), "2 / 2");
    assert.equal(await page.locator("#artwork-dialog").evaluate((dialog) => dialog.open), true, "closing viewer should restore the artist-details dialog");
    await page.locator(".pixiv-viewer-page-button").first().click();
    await page.waitForFunction(() => document.querySelector(".pixiv-viewer-image").naturalWidth === 640);

    await page.evaluate(() => window.PixivCuratorViewer.open({ id: 404, pageIndex: 0, title: "missing", artistName: "Artist", pageCount: 1, rating: "unknown", aiStatus: "unknown", tags: [] }));
    await page.waitForFunction(() => document.querySelector(".pixiv-viewer-status").textContent.includes("读取失败"));
    assert.equal(await page.locator(".pixiv-viewer-title").textContent(), "missing");
    assert.equal(await page.locator(".pixiv-viewer-link").first().getAttribute("href"), "https://www.pixiv.net/artworks/404");
    assert.equal(await page.locator(".pixiv-viewer-link").nth(1).getAttribute("href"), "/admin/api/previews/404/0?size=original", "error should preserve the fixed image link");
    assert.equal(requests.some((entry) => entry.startsWith("/i/") || entry.startsWith("/random")), false, "viewer must never use public randomized image endpoints");
    assert.equal(await page.evaluate(() => window.PixivCuratorViewer.open({ id: "https://evil.example/1", pageIndex: 0, pageCount: 1 })), false, "external image targets must be rejected");

    await page.evaluate(() => window.PixivCuratorViewer.open({ id: 555, pageIndex: 0, title: "slow", artistName: "A", pageCount: 1 }));
    await slowStartedPromise;
    await page.evaluate(() => window.PixivCuratorViewer.open({ id: 777, pageIndex: 0, title: "latest", artistName: "B", pageCount: 1 }));
    await page.waitForFunction(() => document.querySelector(".pixiv-viewer-image").naturalWidth === 777);
    assert.ok(failedRequests.some((url) => url.includes("/admin/api/previews/555/0")), "changing works should cancel the old image request");
    assert.equal(await page.locator(".pixiv-viewer-title").textContent(), "latest", "late old image must not replace current state");

    await page.keyboard.press("Escape");
    await page.waitForFunction(() => !document.querySelector(".pixiv-viewer-dialog").open);
    assert.equal(await page.locator(".pixiv-viewer-image").evaluate((image) => image.hasAttribute("src")), false, "closing viewer should release the image source");
    assert.equal(await page.locator("#artwork-dialog").evaluate((dialog) => dialog.open), true, "Escape should return to artist details");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "viewer should fit the desktop viewport");
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "viewer should fit the mobile viewport");
    assert.deepEqual(errors, [], "browser should report no page errors");
    console.log("Image viewer regression passed: lazy load, metadata, pages, cancellation, error state, links, Escape, and mobile layout.");
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => { console.error(error); process.exitCode = 1; });