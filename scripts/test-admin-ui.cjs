// Offline browser regression. Requires Playwright; no Pixiv requests or real data.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const { chromium } = require(process.env.PIXIV_CURATOR_PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const html = fs.readFileSync(path.join(root, "internal/admin/templates/home.html"), "utf8")
  .replace('{{define "home.html"}}', "").replace("{{end}}", "")
  .replace("{{.CSS}}", () => fs.readFileSync(path.join(root, "internal/admin/static/admin.css"), "utf8"))
  .replace("{{.JS}}", () => fs.readFileSync(path.join(root, "internal/admin/static/admin.js"), "utf8"));
const artists = Array.from({ length: 57 }, (_, index) => ({ id: String(index + 1), name: "画师 " + (index + 1), account: "artist", review_status: "approved", collection_enabled: true, tags: ["风景"] }));
const hits = [];
const preferenceWrites = [];
let savedPreferences = { version: 7, rules: { allowed_ratings: ["safe"], allowed_ai: ["non_ai"], min_width: 0, min_height: 0, orientation: "any", strategy: "balanced", included_tags: [], excluded_tags: [] } };
const recommendationItems = [
  { artist_id: "801", artist: { id: "801", name: "全年龄非 AI" }, score: 0.91, reasons: ["本地兴趣匹配"], sample_illust_ids: ["91001", "91002"], content_rating: "safe", ai_status: "non_ai" },
  { artist_id: "802", artist: { id: "802", name: "成人 AI" }, score: 0.82, reasons: ["分类组合"], sample_illust_ids: ["92001"], content_rating: "adult", ai_status: "ai" },
  { artist_id: "803", artist: { id: "803", name: "未知分类" }, score: 0.73, reasons: ["保留未知"], sample_illust_ids: ["93001"], content_rating: "unknown", ai_status: "unknown" },
  { artist_id: "804", artist: { id: "804", name: "成人 AI 未知" }, score: 0.64, reasons: ["交叉筛选"], sample_illust_ids: ["94001"], content_rating: "adult", ai_status: "unknown" }
];
const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost"); hits.push(url.pathname + url.search);
  res.setHeader("Cache-Control", "no-store");
  if (url.pathname === "/admin/") { res.setHeader("Content-Type", "text/html; charset=utf-8"); return res.end(html); }
  if (url.pathname.startsWith("/admin/api/previews/")) {
    res.setHeader("Content-Type", "image/png");
    return res.end(Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/CukAAAAASUVORK5CYII=", "base64"));
  }
  res.setHeader("Content-Type", "application/json");
  let payload;
  if (url.pathname === "/admin/api/recommendations" && req.method === "POST") {
    payload = { run_id: "run-test" };
    return res.end(JSON.stringify(payload));
  }
  if ((url.pathname === "/admin/api/recommendation-runs/latest" || url.pathname === "/admin/api/recommendations") && req.method === "GET") {
    const rating = url.searchParams.get("content_rating") || "all", ai = url.searchParams.get("ai_type") || "all";
    const filtered = recommendationItems.filter((item) => (rating === "all" || item.content_rating === rating) && (ai === "all" || item.ai_status === ai));
    payload = { run: { id: "run-test", state: "succeeded", request_count: 2 }, items: filtered };
    const delay = rating === "adult" ? 450 : 15;
    return setTimeout(() => { if (!res.destroyed) res.end(JSON.stringify(payload)); }, delay);
  }
  if (url.pathname === "/admin/api/settings/network") { payload = { mode: "direct" }; return res.end(JSON.stringify(payload)); }
  else if (url.pathname === "/admin/api/settings/preferences" && req.method === "GET") { return res.end(JSON.stringify(savedPreferences)); }
  else if (url.pathname === "/admin/api/settings/preferences" && req.method === "PUT") {
    let body = ""; req.on("data", (chunk) => { body += chunk; });
    return req.on("end", () => { const parsed = JSON.parse(body); preferenceWrites.push(parsed); savedPreferences = { version: 8, rules: parsed.rules }; res.end(JSON.stringify({ version: 8 })); });
  }
  if (url.pathname === "/admin/api/library/artists") {
    const page = Number(url.searchParams.get("page")), limit = Number(url.searchParams.get("limit"));
    const source = url.searchParams.get("tag") === "无结果" ? [] : artists;
    payload = { items: source.slice((page - 1) * limit, page * limit), total: source.length, library_total: artists.length, total_pages: Math.ceil(source.length / limit) };
  } else if (url.pathname === "/admin/api/library/tags") payload = { items: ["风景", "无结果"] };
  else if (url.pathname === "/admin/api/accounts") payload = { items: [{ id: "test", name: "测试账号" }] };
  else if (url.pathname === "/admin/api/artworks") payload = { items: [{ id: "100", artist_id: url.searchParams.get("artist_id"), title: "测试代表作", visible: true, page_count: 1, rating: "safe", ai_status: "non_ai", tags: ["风景"], pages: [{ page_index: 0, media_state: "indexed" }] }], next_cursor: "" };
  else if (url.pathname === "/admin/api/stats") payload = { image_pages: 1234, artworks: 999, library_artists: 57, accounts: 1, indexed_pages: 1200 };
  else if (url.pathname === "/random") payload = { url: "https://i.pximg.net/img-original/test.jpg", illust_id: "100", page_index: 0 };
  else { res.statusCode = 404; payload = { error: { code: "NOT_FOUND" } }; }
  res.end(JSON.stringify(payload));
});
(async () => {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, ...(process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE ? { executablePath: process.env.PIXIV_CURATOR_BROWSER_EXECUTABLE } : {}) });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = []; page.on("pageerror", (e) => (errors.push(e.message), console.error("PAGE ERROR", e.message)));
    await page.route("**/*", (route) => new URL(route.request().url()).hostname === "127.0.0.1" ? route.continue() : route.abort());
    const base = "http://127.0.0.1:" + server.address().port;
    await page.goto(base + "/admin/");
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 24);
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    assert.equal(await page.locator("img").count(), 0);
    assert.equal(hits.filter((u) => /artworks|previews/.test(u)).length, 0);
    await page.reload();
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 24);
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    assert.equal(hits.filter((u) => /artworks|previews/.test(u)).length, 0);
    await page.locator("#next-artists").click();
    await page.waitForFunction(() => document.querySelector(".artist-card")?.dataset.artistId === "25");
    assert.equal(await page.locator(".artist-card").count(), 24);
    await page.locator("#reload-library").click();
    await page.waitForFunction(() => document.querySelector(".artist-card")?.dataset.artistId === "25");
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    await page.locator("#library-page-number").fill("3");
    await page.locator("#library-page-jump button").click();
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 9);
    assert.equal(await page.locator("#next-artists").isDisabled(), true);
    await page.locator(".artist-detail-link").first().click();
    await page.locator("#artwork-list img").waitFor();
    assert.match(await page.locator("#artist-detail-info").textContent(), /UID 49/);
    assert.equal(await page.locator("#artwork-list img").count(), 1);
    await page.locator("#close-artworks").click();
    await page.waitForFunction(() => !document.querySelector("#artwork-dialog").open && document.querySelectorAll("#artwork-list img").length === 0);
    assert.equal(await page.locator("img").count(), 0);
    const hitsBeforeDiscover = hits.length;

    // Filter before opening Discover so adult-only previews are never requested.
    await page.evaluate(() => {
      const rating = document.querySelector("#recommendation-rating");
      rating.value = "safe";
      rating.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await page.locator("#tab-discover").click();
    await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "全年龄非 AI");
    assert.deepEqual(await page.locator(".recommendation-card .candidate-name").allTextContents(), ["全年龄非 AI"]);
    assert.equal(hits.some((u) => u.startsWith("/admin/api/previews/92001/")), false, "safe filter must not request adult sample previews");
    assert.ok(hits.includes("/admin/api/previews/91001/0") && hits.includes("/admin/api/previews/91002/0"), "sample IDs should map directly to page-zero preview endpoints");
    assert.equal(hits.slice(hitsBeforeDiscover).some((u) => u.startsWith("/admin/api/artworks?")), false, "recommendation samples must not fetch artwork metadata");
    assert.equal(await page.locator(".recommendation-cover").count(), 0);
    const desktopCard = page.locator(".recommendation-card").first();
    const desktopSamples = page.locator(".candidate-samples").first();
    assert.ok((await desktopSamples.boundingBox()).y >= (await desktopCard.locator(".sample-classification").boundingBox()).y, "samples should follow recommendation text");
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 900 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "recommendation horizontal overflow at " + width);
      assert.equal(await desktopCard.evaluate((element) => element.scrollWidth > element.clientWidth), false, "recommendation card overflow at " + width);
      if (width === 1440) await page.screenshot({ path: path.join(root, "data", "recommendation-layout-desktop.png"), fullPage: true });
      else await page.screenshot({ path: path.join(root, "data", "recommendation-layout-mobile.png"), fullPage: true });
    }
    await page.locator("#recommendation-rating").selectOption("unknown");
    await page.locator("#recommendation-ai").selectOption("unknown");
    await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "未知分类");
    assert.deepEqual(await page.locator(".recommendation-card .candidate-name").allTextContents(), ["未知分类"]);
    const unknownRequest = hits.findLast((u) => u.startsWith("/admin/api/recommendation-runs/latest?") && u.includes("content_rating=unknown") && u.includes("ai_type=unknown"));
    assert.ok(unknownRequest, "unknown rating and AI filters should be sent together");
    const runListRequest = page.waitForRequest((request) => request.url().includes("/admin/api/recommendations?") && request.url().includes("run_id=run-test"));
    await page.locator("#start-recommendations").click();
    await runListRequest;
    await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "未知分类");
    assert.ok(hits.some((u) => u.startsWith("/admin/api/recommendations?") && u.includes("run_id=run-test") && u.includes("content_rating=unknown") && u.includes("ai_type=unknown")), "run list requests should include both sample filters");

    // An older, slower response must not replace the latest filter result.
    const adultRequest = page.waitForRequest((request) => request.url().includes("content_rating=adult"));
    await page.locator("#recommendation-rating").selectOption("adult");
    await adultRequest;
    await page.locator("#recommendation-rating").selectOption("safe");
    await page.locator("#recommendation-ai").selectOption("all");
    await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "全年龄非 AI");
    await page.waitForTimeout(550);
    assert.deepEqual(await page.locator(".recommendation-card .candidate-name").allTextContents(), ["全年龄非 AI"], "stale adult response must not overwrite latest result");
    await page.locator("#recommendation-rating").selectOption("adult");
    await page.locator("#recommendation-ai").selectOption("non_ai");
    await page.waitForFunction(() => document.querySelector("#recommendation-list .empty-cell")?.textContent.includes("没有候选"));
    assert.equal(await page.locator(".recommendation-cover").count(), 0, "empty recommendation layout must not render a hero cover");

    await page.locator("#tab-library").click();
    await page.locator("#library-page-size").selectOption("48");
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 48);
    await page.locator("#library-tag").selectOption("无结果");
    await page.waitForFunction(() => document.querySelector("#library-summary").textContent.includes("符合条件 0"));
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    await page.locator("#tab-stats").click();
    await page.locator(".stat-value").first().waitFor();
    assert.equal(await page.locator(".stat-value").first().textContent(), "1,234");
    await page.locator("#api-account").selectOption("test");
    await page.locator("#api-rating").selectOption("0");
    await page.locator("#api-ai").selectOption("0");
    await page.locator("#api-include-tags").fill("猫|狗\n风景");
    await page.locator("#api-format").selectOption("redirect");
    const generated = new URL(await page.locator("#api-url").inputValue());
    assert.equal(generated.searchParams.get("format"), "redirect");
    assert.equal(generated.searchParams.get("delivery"), "proxy");
    assert.equal(generated.searchParams.get("r18"), "0");
    assert.equal(generated.searchParams.get("ai_type"), "0");
    assert.deepEqual(generated.searchParams.getAll("included_tags"), ["猫|狗", "风景"]);
    await page.locator("#test-api").click();
    await page.waitForFunction(() => document.querySelector("#api-result").textContent.includes("i.pximg.net"));
    assert.match(hits.findLast((u) => u.startsWith("/random?")), /format=json/);
    assert.equal(hits.some((u) => /\/random\?[^ ]*format=image/.test(u)), false, "test action must not request image bytes");
    await page.locator("#api-rating").selectOption("unknown");
    await page.locator("#api-ai").selectOption("unknown");
    let apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("r18"), "unknown");
    assert.equal(apiURL.searchParams.get("ai_type"), "unknown");
    await page.locator("#api-delivery").selectOption("original");
    apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("delivery"), "original");
    await page.locator("#api-format").selectOption("image");
    apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("format"), "image");
    assert.equal(apiURL.searchParams.get("delivery"), "proxy", "image format should force proxy delivery");
    assert.equal(await page.locator('#api-delivery option[value="original"]').evaluate((option) => option.disabled), true);

    await page.locator("#tab-settings").click();
    await page.locator('#preferences-form input[name="rating"][value="unknown"]').check();
    await page.locator('#preferences-form input[name="ai"][value="unknown"]').check();
    await page.locator('#preferences-form button[type="submit"]').click();
    await page.waitForFunction(() => document.querySelector("#preference-version").textContent.includes("8"));
    assert.deepEqual(preferenceWrites.at(-1).rules.allowed_ratings, ["safe", "unknown"]);
    assert.deepEqual(preferenceWrites.at(-1).rules.allowed_ai, ["non_ai", "unknown"]);
    await page.reload();
    await page.locator("#tab-settings").click();
    await page.waitForFunction(() => document.querySelector('#preferences-form input[name="rating"][value="unknown"]').checked && document.querySelector('#preferences-form input[name="ai"][value="unknown"]').checked);
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "horizontal overflow at " + width);
    }
    assert.deepEqual(errors, []);
    console.log("PASS: count, pagination, no initial images/requests, details, filters, statistics, API builder/test, responsive layout");
  } finally {
    await browser?.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((e) => { console.error(e); process.exitCode = 1; });
