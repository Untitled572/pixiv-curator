// Offline browser regression. Requires Playwright; no Pixiv requests or real data.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const http = require("node:http");
const { chromium } = require(process.env.PIXIV_CURATOR_PLAYWRIGHT_MODULE || "playwright");
const root = path.resolve(__dirname, "..");
const html = fs.readFileSync(path.join(root, "internal/admin/templates/home.html"), "utf8")
  .replace('{{define "home.html"}}', "").replace("{{end}}", "")
  .replace("{{.CSS}}", () => fs.readFileSync(path.join(root, "internal/admin/static/admin.css"), "utf8") + "\n" + fs.readFileSync(path.join(root, "internal/admin/static/viewer.css"), "utf8"))
  .replace("{{.JS}}", () => fs.readFileSync(path.join(root, "internal/admin/static/viewer.js"), "utf8") + "\n" + fs.readFileSync(path.join(root, "internal/admin/static/admin.js"), "utf8"));
const artists = Array.from({ length: 57 }, (_, index) => ({ id: String(index + 1), name: "画师 " + (index + 1), account: "artist", review_status: "approved", collection_enabled: true, tags: ["风景"] }));
const tagCorpus = ["风景", "无结果", "搜索专用", ...Array.from({ length: 15139 }, (_, index) => `测试标签${String(index).padStart(5, "0")}`)];
const hits = [];
const preferenceWrites = [];
let savedPreferences = { version: 7, rules: { allowed_ratings: ["safe"], allowed_ai: ["non_ai"], exclude_manga: false, min_width: 0, min_height: 0, orientation: "any", strategy: "balanced", included_tags: [], excluded_tags: [] } };
const recommendationItems = [
  { artist_id: "801", artist: { id: "801", name: "全年龄非 AI" }, score: 0.91, reasons: ["本地兴趣匹配"], sample_illust_ids: ["91001", "91002"], content_rating: "safe", ai_status: "non_ai" },
  { artist_id: "802", artist: { id: "802", name: "成人 AI" }, score: 0.82, reasons: ["分类组合"], sample_illust_ids: ["92001"], content_rating: "adult", ai_status: "ai" },
  { artist_id: "803", artist: { id: "803", name: "未知分类" }, score: 0.73, reasons: ["保留未知"], sample_illust_ids: ["93001"], content_rating: "unknown", ai_status: "unknown" },
  { artist_id: "804", artist: { id: "804", name: "成人 AI 未知" }, score: 0.64, reasons: ["交叉筛选"], sample_illust_ids: ["94001"], content_rating: "adult", ai_status: "unknown" }
];
const completedRunItems = Array.from({ length: 55 }, (_, index) => ({
  artist_id: String(1001 + index), artist: { id: String(1001 + index), name: `完成候选 ${index + 1}` },
  exploration: index >= 24, score: 0.9 - index / 100, reasons: ["本地兴趣匹配"], sample_illust_ids: [String(100001 + index * 3), String(100002 + index * 3), String(100003 + index * 3)], content_rating: "unknown", ai_status: "unknown"
}));
let newRunPolls = 0;
let newRunActive = false;
let refillPhase = "";
const refillCarryIDs = completedRunItems.slice(1, 16).map((item) => item.artist_id);
const refillCarryItems = completedRunItems.slice(1, 16).map((item) => ({ ...item, components: { carryover: true } }));
const refillNewItems = completedRunItems.slice(30, 45);
let failNextApprove = false;
let activeNewPreviewRequests = 0, maxNewPreviewRequests = 0, newRunPreviewRequests = 0;
let resolveFirstNewPreview;
const firstNewPreviewSeen = new Promise((resolve) => { resolveFirstNewPreview = resolve; });
const accountWrites = [];
let accounts = [
  { id: "test", name: "主账号", status: "ready", sync_state: "succeeded", is_default: true, follow_on_approve: false, followed_artists: 3, pending_follows: 0 },
  { id: "second", name: "副账号", status: "ready", sync_state: "succeeded", is_default: false, follow_on_approve: false, followed_artists: 2, pending_follows: 0 }
];
const historyItems = [
  { id: "event-2", selected_at: 1730000002000, illust_id: "7001", page_index: 0, artist_id: "70", artist_name: "历史画师", title: "重复身份作品", width: 1200, height: 900, rating: "safe", ai_status: "ai", tags: ["AIイラスト"], available: true },
  { id: "event-1", selected_at: 1730000001000, illust_id: "7001", page_index: 0, artist_id: "70", artist_name: "历史画师", title: "重复身份作品", width: 1200, height: 900, rating: "safe", ai_status: "ai", tags: ["AIイラスト"], available: true },
  { id: "event-blocked", selected_at: 1730000000000, illust_id: "7002", page_index: 0, artist_id: "71", artist_name: "已屏蔽画师", title: "不可预览作品", width: 800, height: 600, available: false }
];
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/CukAAAAASUVORK5CYII=", "base64");
const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://localhost"); hits.push(url.pathname + url.search);
  res.setHeader("Cache-Control", "no-store");
  if (url.pathname === "/admin/") { res.setHeader("Content-Type", "text/html; charset=utf-8"); return res.end(html); }
  if (url.pathname.startsWith("/admin/api/previews/")) {
    res.setHeader("Content-Type", "image/png");
    const id = url.pathname.split("/")[4] || "";
    if (/^1000[0-9]+$/.test(id)) {
      activeNewPreviewRequests++; newRunPreviewRequests++; maxNewPreviewRequests = Math.max(maxNewPreviewRequests, activeNewPreviewRequests); resolveFirstNewPreview();
      return setTimeout(() => { activeNewPreviewRequests--; if (!res.destroyed) res.end(png); }, 180);
    }
    return res.end(png);
  }
  res.setHeader("Content-Type", "application/json");
  let payload;
  if (url.pathname === "/admin/api/recommendations" && req.method === "POST") {
    newRunActive = true; newRunPolls = 0; newRunPreviewRequests = 0; maxNewPreviewRequests = 0; activeNewPreviewRequests = 0;
    payload = { run_id: "run-new" };
    return res.end(JSON.stringify(payload));
  }
  if (/^\/admin\/api\/artists\/\d+\/approve$/.test(url.pathname) && req.method === "POST") {
    if (failNextApprove) { failNextApprove = false; res.statusCode = 500; return res.end(JSON.stringify({ error: { code: "NOT_READY" } })); }
    return setTimeout(() => { if (!res.destroyed) res.end(JSON.stringify({ updated: true })); }, 100);
  }
  if ((url.pathname === "/admin/api/recommendation-runs/latest" || url.pathname === "/admin/api/recommendations") && req.method === "GET") {
    if (refillPhase) {
      const running = refillPhase === "running";
      payload = { run: { id: "run-refilled", state: running ? "running" : "succeeded", request_count: running ? 2 : 3, progress_percent: running ? 35 : 100 }, items: running ? refillCarryItems : [...refillCarryItems, ...refillNewItems] };
      return setTimeout(() => { if (!res.destroyed) res.end(JSON.stringify(payload)); }, 30);
    }
    if (url.searchParams.get("run_id") === "run-new" || (url.pathname === "/admin/api/recommendation-runs/latest" && newRunActive)) {
      newRunPolls++;
      const running = newRunPolls <= 2;
      payload = { run: { id: "run-new", state: running ? "running" : "succeeded", request_count: newRunPolls, progress_percent: running ? newRunPolls * 10 : 100 }, items: running ? completedRunItems.slice(0, 3) : completedRunItems };
      return setTimeout(() => { if (!res.destroyed) res.end(JSON.stringify(payload)); }, 30);
    }
    const rating = url.searchParams.get("content_rating") || "all", ai = url.searchParams.get("ai_type") || "all";
    const filtered = recommendationItems.filter((item) => (rating === "all" || item.content_rating === rating) && (ai === "all" || item.ai_status === ai));
    payload = { run: { id: "run-test", state: "succeeded", request_count: 2, progress_percent: 100 }, items: filtered };
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
    let source = url.searchParams.get("tag") === "无结果" ? [] : artists;
    if (url.searchParams.has("collection_enabled")) source = source.filter((artist) => artist.collection_enabled === (url.searchParams.get("collection_enabled") === "1"));
    payload = { items: source.slice((page - 1) * limit, page * limit), total: source.length, library_total: artists.length, total_pages: Math.ceil(source.length / limit) };
  } else if (url.pathname === "/admin/api/library/tags") {
    const q = url.searchParams.get("q") || "", limit = Math.min(30, Math.max(1, Number(url.searchParams.get("limit")) || 30));
    payload = { items: tagCorpus.filter((tag) => tag.includes(q)).slice(0, limit) };
  } else if (url.pathname === "/admin/api/accounts" && req.method === "GET") payload = { items: accounts };
  else if (url.pathname.match(/^\/admin\/api\/accounts\/[^/]+$/) && req.method === "PATCH") {
    let body = ""; req.on("data", (chunk) => { body += chunk; });
    return req.on("end", () => {
      const parsed = JSON.parse(body), id = decodeURIComponent(url.pathname.split("/").at(-1)); accountWrites.push({ path: url.pathname, method: req.method, body: parsed });
      if (parsed.is_default === true) accounts = accounts.map((account) => ({ ...account, is_default: account.id === id }));
      else if (Object.hasOwn(parsed, "follow_on_approve")) accounts = accounts.map((account) => account.id === id ? { ...account, follow_on_approve: parsed.follow_on_approve } : account);
      res.end(JSON.stringify({ updated: true }));
    });
  } else if (url.pathname.endsWith("/copy-following") && req.method === "POST") {
    let body = ""; req.on("data", (chunk) => { body += chunk; });
    return req.on("end", () => { accountWrites.push({ path: url.pathname, method: req.method, body: JSON.parse(body) }); res.end(JSON.stringify({ queued: 2 })); });
  } else if (url.pathname === "/admin/api/history") {
    payload = { items: historyItems, total: historyItems.length, page: Number(url.searchParams.get("page")) || 1, page_size: Number(url.searchParams.get("limit")) || 24, total_pages: 1 };
  }
  else if (url.pathname === "/admin/api/artworks") payload = { items: [{ id: "100", artist_id: url.searchParams.get("artist_id"), title: "测试代表作", visible: true, page_count: 1, rating: "safe", ai_status: "non_ai", tags: ["风景"], pages: [{ page_index: 0, media_state: "indexed" }] }], next_cursor: "" };
  else if (url.pathname === "/admin/api/stats") payload = { image_pages: 1234, artworks: 999, library_artists: 57, accounts: 1, indexed_pages: 1200 };
  else if (url.pathname === "/random") {
    const format = url.searchParams.get("format") || "json";
    if (format === "image") { res.setHeader("Content-Type", "image/png"); return res.end(png); }
    if (format === "redirect") { res.statusCode = 302; res.setHeader("Location", "/mock-image.png"); return res.end(); }
    payload = { url: "https://i.pximg.net/img-original/test.jpg", illust_id: "100", page_index: 0 };
  } else if (url.pathname === "/mock-image.png") { res.setHeader("Content-Type", "image/png"); return res.end(png); }
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
    await page.locator("#panel-stats .stat-value").first().waitFor();
    assert.equal(await page.locator("#current-page-title").textContent(), "统计与接口");
    assert.equal(await page.locator("#tab-stats").getAttribute("aria-selected"), "true");
    assert.equal(hits.some((hit) => hit.startsWith("/admin/api/library/artists?") || hit.startsWith("/admin/api/library/tags?")), false, "default Stats page must not fetch library data");
    assert.equal(hits.some((hit) => hit.startsWith("/admin/api/previews/")), false, "default Stats page must not request image previews");
    assert.equal(await page.locator("img").count(), 0, "default Stats page should not create image elements");
    await page.locator("#tab-library").click();
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 24);
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    assert.equal(tagCorpus.length, 15142);
    assert.ok(hits.some((hit) => hit.startsWith("/admin/api/library/tags?limit=30&q=")), "library tag options should use bounded q/limit lookup");
    assert.ok(await page.locator("#library-tag option").count() <= 32, "tag options should be capped at 30 plus the all/current entries");
    assert.ok(await page.locator("body *").count() < 1500, "large tag corpus must not expand the DOM");
    assert.equal(await page.locator("img").count(), 0);
    assert.equal(hits.filter((u) => /artworks|previews/.test(u)).length, 0);
    const tagSearch = page.waitForResponse((response) => response.url().includes("/admin/api/library/tags?") && new URL(response.url()).searchParams.get("q") === "搜索专用");
    await page.locator("#library-tag-search").fill("搜索专用");
    await tagSearch;
    assert.equal(await page.locator("#library-tag option").count(), 2);
    assert.equal(await page.locator("#library-tag option").nth(1).textContent(), "搜索专用");
    assert.ok(hits.includes("/admin/api/library/tags?limit=30&q=%E6%90%9C%E7%B4%A2%E4%B8%93%E7%94%A8"));
    const allTags = page.waitForResponse((response) => response.url().includes("/admin/api/library/tags?") && new URL(response.url()).searchParams.get("q") === "");
    await page.locator("#library-tag-search").fill(""); await allTags;
    const libraryHitsBeforeReload = hits.filter((hit) => hit.startsWith("/admin/api/library/artists?") || hit.startsWith("/admin/api/library/tags?")).length;
    await page.reload();
    await page.locator("#panel-stats .stat-value").first().waitFor();
    assert.equal(hits.filter((hit) => hit.startsWith("/admin/api/library/artists?") || hit.startsWith("/admin/api/library/tags?")).length, libraryHitsBeforeReload, "reloaded admin page returns to Stats without loading the library");
    const libraryHitsBeforeReloadVisit = libraryHitsBeforeReload;
    await page.locator("#tab-library").click();
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 24);
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    assert.ok(hits.filter((hit) => hit.startsWith("/admin/api/library/artists?") || hit.startsWith("/admin/api/library/tags?")).length > libraryHitsBeforeReloadVisit, "library data loads after navigating back to its tab");
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
    const artworkTitleViewerRequest = page.waitForRequest((request) => request.url().includes("/admin/api/previews/100/0?size=original"));
    await page.locator("#artwork-list .artwork-title-button").click(); await artworkTitleViewerRequest;
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), "/admin/api/previews/100/0?size=original", "library artwork title opens the same viewer");
    await page.locator(".pixiv-viewer-image").click();
    assert.equal(await page.locator(".pixiv-viewer-dialog").evaluate((dialog) => dialog.open), true, "clicking the image inside the viewer does not close it");
    const viewerBounds = await page.locator(".pixiv-viewer-dialog").boundingBox();
    await page.mouse.click(Math.max(0, viewerBounds.x - 10), viewerBounds.y + viewerBounds.height / 2);
    await page.waitForFunction(() => !document.querySelector(".pixiv-viewer-dialog").open);
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), null, "backdrop close stops and clears the viewer image request");
    const detailViewerRequest = page.waitForRequest((request) => request.url().includes("/admin/api/previews/100/0?size=original"));
    await page.locator("#artwork-list .artist-cover").click();
    await detailViewerRequest;
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), "/admin/api/previews/100/0?size=original");
    assert.equal(page.url(), base + "/admin/", "viewer thumbnail click must stay inside Curator");
    await page.locator(".pixiv-viewer-close").click();
    await page.locator("#artist-detail-info").click();
    assert.equal(await page.locator("#artwork-dialog").evaluate((dialog) => dialog.open), true, "clicking inside the artist detail dialog does not close it");
    const artworkDialogBounds = await page.locator("#artwork-dialog").boundingBox();
    await page.mouse.click(Math.max(0, artworkDialogBounds.x - 10), artworkDialogBounds.y + artworkDialogBounds.height / 2);
    await page.waitForFunction(() => !document.querySelector("#artwork-dialog").open && document.querySelectorAll("#artwork-list img").length === 0);
    assert.equal(await page.locator("img").count(), 1, "closing the viewer should keep its UI shell but clear the image request");
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), null, "closing the viewer must stop and clear the original-image request");
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
    const discoveryTitleRequest = page.waitForRequest((request) => request.url().includes("/admin/api/previews/91001/0?size=original"));
    await page.locator(".recommendation-card .candidate-title-button").click(); await discoveryTitleRequest;
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), "/admin/api/previews/91001/0?size=original", "discovery artist name opens the first sample in the same viewer");
    await page.locator(".pixiv-viewer-close").click();
    const desktopCard = page.locator(".recommendation-card").first();
    const desktopSamples = page.locator(".candidate-samples").first();
    assert.ok((await desktopSamples.boundingBox()).y < (await desktopCard.locator(".candidate-name").boundingBox()).y, "samples should appear above the artist name");
    assert.ok((await desktopSamples.locator(".sample-tile").first().boundingBox()).height >= 200, "discovery previews should be larger");
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
    const unknownRequest = hits.findLast((u) => u === "/admin/api/recommendation-runs/latest");
    assert.ok(unknownRequest, "result reads must remain unfiltered; filtering happens in the browser");
	await page.locator(".recommendation-card").first().evaluate((card) => { window.__originalRecommendationCard = card; });
	const sameRunRefresh = page.waitForRequest((request) => request.url().includes("/admin/api/recommendation-runs/latest"));
	await page.locator("#tab-discover").click(); await sameRunRefresh;
	await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "未知分类");
	assert.ok(await page.locator(".recommendation-card").first().evaluate((card) => card.isSameNode(window.__originalRecommendationCard)), "refreshing identical recommendations must preserve existing card nodes");
	assert.equal(await page.locator("#recommendation-status .run-meta").textContent(), "100%", "a completed run refresh keeps the final progress at 100 percent");
	assert.equal(await page.locator("#start-recommendations").isDisabled(), false, "completed discovery re-enables the start button");

    assert.equal(hits.some((u) => /recommendation.*[?&](content_rating|ai_type)=/.test(u)), false, "display filters must never be sent to discovery or polling");
    // An older, slower response must not replace the latest filter result.
    const adultRequest = page.waitForRequest((request) => request.url().includes("/admin/api/recommendation-runs/latest"));
    await page.locator("#recommendation-rating").selectOption("adult");
    await adultRequest;
    await page.locator("#recommendation-rating").selectOption("safe");
    await page.locator("#recommendation-ai").selectOption("all");
    await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "全年龄非 AI");
    await page.waitForTimeout(550);
    assert.deepEqual(await page.locator(".recommendation-card .candidate-name").allTextContents(), ["全年龄非 AI"], "stale adult response must not overwrite latest result");
    await page.locator("#recommendation-rating").selectOption("adult");
    await page.locator("#recommendation-ai").selectOption("non_ai");
	await page.waitForFunction(() => document.querySelector("#recommendation-list .empty-cell")?.textContent.includes("没有带代表作的候选"));
    assert.equal(await page.locator(".recommendation-cover").count(), 0, "empty recommendation layout must not render a hero cover");
	await page.locator("#recommendation-rating").selectOption("unknown");
	await page.locator("#recommendation-ai").selectOption("unknown");
	await page.waitForFunction(() => document.querySelector(".recommendation-card .candidate-name")?.textContent === "未知分类");
	const beforeNewRunPreviews = hits.filter((hit) => /^\/admin\/api\/previews\/1000[0-9]+/.test(hit)).length;
	await page.locator("#start-recommendations").click();
	await page.waitForFunction(() => document.querySelector("#recommendation-status .run-meta")?.textContent === "10%");
	assert.equal(await page.locator("#start-recommendations").isDisabled(), true, "a running recommendation job cannot be started again");
	assert.equal(await page.locator(".recommendation-card").count(), 0, "running jobs must not display partial candidates");
	const runningPlaceholderText = await page.locator("#recommendation-list").evaluate((list) => { window.__runningRecommendationPlaceholder = list.firstElementChild; return list.firstElementChild?.textContent || ""; });
	assert.ok(runningPlaceholderText.includes("正在收集候选"));
	assert.equal(hits.filter((hit) => /^\/admin\/api\/previews\/1000[0-9]+/.test(hit)).length, beforeNewRunPreviews, "partial candidate samples must not request previews");
	assert.equal(hits.filter((hit) => hit === "/admin/api/recommendations").length, 1, "the new recommendation job should be submitted once");
	await page.evaluate(() => document.querySelector("#start-recommendations").click());
	assert.equal(hits.filter((hit) => hit === "/admin/api/recommendations").length, 1, "disabled start action must not submit a duplicate job");
	await page.waitForFunction(() => document.querySelector("#recommendation-status .run-meta")?.textContent === "20%");
	assert.ok(await page.locator("#recommendation-list").evaluate((list) => list.firstElementChild.isSameNode(window.__runningRecommendationPlaceholder)), "polling running status must not rebuild the candidate list (old=" + await page.evaluate(() => window.__runningRecommendationPlaceholder?.textContent) + ", new=" + await page.locator("#recommendation-list").textContent() + ")");
	assert.equal(await page.locator(".recommendation-card").count(), 0, "the second running response must still hide partial candidates");
	assert.equal(hits.filter((hit) => /^\/admin\/api\/previews\/1000[0-9]+/.test(hit)).length, beforeNewRunPreviews, "running partial samples must remain unfetched");
	await firstNewPreviewSeen;
	assert.equal(newRunPolls, 3, "sample loading should only begin after the succeeded response");
	assert.equal(await page.locator(".recommendation-card").count(), 0, "slow sample loading must not insert a partial batch");
	await page.waitForFunction(() => document.querySelectorAll(".recommendation-card").length === 30, null, { timeout: 10000 });
	assert.equal(await page.locator(".recommendation-card").count(), 30, "completed results should cap rendered candidates at 30");
	assert.equal(await page.locator(".sample-tile").count(), 90, "each rendered candidate may show at most three samples");
	assert.equal(newRunPreviewRequests, 90, "only the first 30 candidates should load three previews each");
	assert.ok(maxNewPreviewRequests <= 4 && maxNewPreviewRequests >= 2, `preview concurrency should be between 2 and 4, got ${maxNewPreviewRequests}`);
	assert.equal(activeNewPreviewRequests, 0, "all prepared previews should finish before insertion");
    assert.equal(await page.locator('.recommendation-card[data-exploration="true"]').count(), 6);
    assert.equal(await page.locator(".exploration-chip").count(), 0, "exploration candidates share the normal recommendation presentation");
    const failedExploration = page.locator('.recommendation-card[data-exploration="true"]').first();
    failNextApprove = true;
    await failedExploration.locator("[data-approve]").click();
    await page.waitForFunction((card) => !card.querySelector("[data-approve]")?.disabled, await failedExploration.elementHandle());
    assert.equal(await failedExploration.count(), 1, "failed approval keeps the exploration card");
    assert.equal(await page.locator(".exploration-particle-overlay").count(), 0, "failed approval emits no particles");
    const successExploration = page.locator('.recommendation-card[data-exploration="true"]').nth(1);
    const approveResponse = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith("/admin/api/artists/1026/approve"));
    const successCardBounds = await successExploration.boundingBox();
    await successExploration.locator("[data-approve]").click();
    assert.equal(await successExploration.locator("button").filter({ hasText: "不感兴趣" }).isDisabled(), true, "both decisions are locked while approval is pending");
    assert.equal(await successExploration.locator("[data-approve]").isDisabled(), true);
    await approveResponse;
    const particleOverlay = page.locator(".exploration-particle-overlay");
    await particleOverlay.waitFor();
    assert.equal(await particleOverlay.locator(".exploration-particle").count(), 96, "successful exploration approval emits a 12 by 8 particle grid");
    assert.equal(await page.locator(".exploration-card-glow").count(), 0, "exploration particle effect creates no green card glow node");
    assert.equal(await particleOverlay.evaluate((overlay) => getComputedStyle(overlay).position), "fixed");
    assert.equal(await particleOverlay.evaluate((overlay) => getComputedStyle(overlay).pointerEvents), "none");
    const overlayBounds = await particleOverlay.boundingBox();
    assert.ok(Math.abs(overlayBounds.width - successCardBounds.width) < 2 && Math.abs(overlayBounds.height - successCardBounds.height) < 2, "effect covers the whole artist card");
    const spread = await particleOverlay.locator(".exploration-particle").evaluateAll((particles) => {
      const xs = particles.map((p) => parseFloat(p.style.left)), ys = particles.map((p) => parseFloat(p.style.top));
      return { x: Math.max(...xs) - Math.min(...xs), y: Math.max(...ys) - Math.min(...ys), minSize: Math.min(...particles.map((p) => parseFloat(getComputedStyle(p).width))) };
    });
    assert.ok(spread.x > 70 && spread.y > 70 && spread.minSize >= 8, "large particles span the card width and height");
    assert.equal(await successExploration.count(), 1, "successful exploration approval shows a brief confirmation before removal");
    await particleOverlay.waitFor({ state: "detached" });
    const normalCard = page.locator('.recommendation-card[data-exploration="false"]').first();
    const normalHandle = await normalCard.elementHandle();
    await normalCard.locator("[data-approve]").click();
    await page.waitForFunction((card) => !card.isConnected, normalHandle);
    assert.equal(await page.locator(".exploration-particle-overlay").count(), 0, "normal recommendations emit no particles");
    await page.emulateMedia({ reducedMotion: "reduce" });
    const reducedCard = page.locator('.recommendation-card[data-exploration="true"]').first();
    const reducedHandle = await reducedCard.elementHandle();
    await reducedCard.locator("[data-approve]").click();
    await page.waitForFunction((card) => !card.isConnected, reducedHandle);
    assert.equal(await page.locator(".exploration-particle-overlay").count(), 0, "reduced-motion approval emits no particles");
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const carrySampleIDs = completedRunItems.slice(1, 16).flatMap((item) => item.sample_illust_ids);
    const carryPreviewHits = hits.filter((hit) => carrySampleIDs.some((id) => hit.startsWith(`/admin/api/previews/${id}/`))).length;
    await page.locator(".recommendation-card").evaluateAll((cards) => {
      window.__refillCarryCards = cards.slice(0, 15);
      cards.slice(15).forEach((card) => card.remove());
    });
    assert.equal(await page.locator(".recommendation-card").count(), 15, "refill test starts with 15 existing cards");
    refillPhase = "running";
    await page.locator("#tab-discover").click();
    await page.waitForFunction(() => document.querySelector("#recommendation-status .run-meta")?.textContent === "35%");
    assert.equal(await page.locator(".recommendation-card").count(), 15, "running refill keeps the existing cards visible");
    assert.ok(await page.evaluate(() => window.__refillCarryCards.every((card) => card.isConnected && document.querySelector(`.recommendation-card[data-artist-id="${CSS.escape(card.dataset.artistId)}"]`) === card)), "running refill preserves the original card nodes");
    assert.equal(hits.filter((hit) => carrySampleIDs.some((id) => hit.startsWith(`/admin/api/previews/${id}/`))).length, carryPreviewHits, "running refill does not reload previews for existing cards");
    refillPhase = "succeeded";
    await page.locator("#tab-discover").click();
    await page.waitForFunction(() => document.querySelectorAll(".recommendation-card").length === 30, null, { timeout: 10000 });
    const carriedCardStatus = await page.evaluate(() => ({ original: window.__refillCarryCards.map((card) => [card.dataset.artistId, card.isConnected]), current: Array.from(document.querySelectorAll(".recommendation-card")).map((card) => card.dataset.artistId) }));
    assert.ok(await page.evaluate(() => window.__refillCarryCards.every((card) => card.isConnected && document.querySelector(`.recommendation-card[data-artist-id="${CSS.escape(card.dataset.artistId)}"]`) === card)), "completed refill reuses the original carried card nodes: " + JSON.stringify(carriedCardStatus));
    assert.deepEqual(await page.locator(".recommendation-card").evaluateAll((cards) => cards.map((card) => card.dataset.artistId)), [...refillCarryIDs, ...refillNewItems.map((item) => item.artist_id)], "completed refill combines carried and new candidates");
    assert.equal(hits.filter((hit) => carrySampleIDs.some((id) => hit.startsWith(`/admin/api/previews/${id}/`))).length, carryPreviewHits, "completed refill does not reload previews for carried cards");
    refillPhase = "";
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.screenshot({path:path.join(root,"data","discovery-three-previews.png"),fullPage:false});
	await page.setViewportSize({ width: 1440, height: 900 });

    await page.locator("#tab-library").click();
    await page.locator("#library-collection").selectOption("0");
    await page.waitForFunction(() => document.querySelector("#library-summary").textContent.includes("符合条件 0"));
    assert.ok(hits.some((hit) => hit.startsWith("/admin/api/library/artists?") && hit.includes("collection_enabled=0")));
    await page.locator("#library-collection").selectOption("");
    await page.locator(".artist-card").first().waitFor();
    const positions = await page.locator(".artist-card").first().locator(".artist-actions > button, .artist-actions summary").evaluateAll((items) => items.map((item) => ({ y: item.getBoundingClientRect().y, right: item.getBoundingClientRect().right, left: item.getBoundingClientRect().left })));
    assert.equal(positions.length, 4);
    assert.ok((await page.locator(".artist-card").first().boundingBox()).height < 105, "desktop library rows should remain compact");
    await page.screenshot({path:path.join(root,"data","library-compact-rows.png"),fullPage:false});
    assert.ok(positions.every((p) => Math.abs(p.y - positions[0].y) < 2), "tag editor and three actions should share one row");
    await page.locator("#library-page-size").selectOption("48");
    await page.waitForFunction(() => document.querySelectorAll(".artist-card").length === 48);
    await page.locator("#library-tag").selectOption("无结果");
    await page.waitForFunction(() => document.querySelector("#library-summary").textContent.includes("符合条件 0"));
    assert.equal(await page.locator("#artist-count").textContent(), "57");
    await page.locator("#tab-history").click();
    await page.waitForFunction(() => document.querySelectorAll(".history-card").length === 3);
    assert.ok(hits.some((hit) => hit === "/admin/api/history?page=1&limit=24"));
    assert.deepEqual(await page.locator(".history-card").evaluateAll((cards) => cards.map((card) => card.dataset.historyId)), ["event-2", "event-1", "event-blocked"], "duplicate selections must remain separate history events");
    const firstHistoryImage = page.locator('[data-history-id="event-2"] .history-preview img');
    const secondHistoryImage = page.locator('[data-history-id="event-1"] .history-preview img');
    await firstHistoryImage.waitFor(); await secondHistoryImage.waitFor();
    assert.equal(await firstHistoryImage.getAttribute("src"), "/admin/api/previews/7001/0");
    assert.equal(await secondHistoryImage.getAttribute("src"), "/admin/api/previews/7001/0", "separate history events may share one fixed image identity");
    const historyTitleViewerRequest = page.waitForRequest((request) => request.url().includes("/admin/api/previews/7001/0?size=original"));
    await page.locator('[data-history-id="event-2"] .artwork-title-button').click(); await historyTitleViewerRequest;
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), "/admin/api/previews/7001/0?size=original", "history title opens the same viewer");
    assert.ok((await page.locator(".pixiv-viewer-classification").textContent()).includes("AI"), "history title passes the known AI classification");
    assert.ok((await page.locator(".pixiv-viewer-classification").textContent()).includes("全年龄"), "history title passes the known rating");
    assert.deepEqual(await page.locator(".pixiv-viewer-tag").allTextContents(), ["AIイラスト"], "history title passes known tags");
    await page.locator(".pixiv-viewer-close").click();
    const blockedHistory = page.locator('[data-history-id="event-blocked"] .history-preview');
    assert.equal(await blockedHistory.isDisabled(), true, "blocked history item cannot be previewed");
    assert.equal(hits.some((hit) => hit.startsWith("/admin/api/previews/7002/")), false, "blocked history metadata must not request preview bytes");
    const historyViewerRequest = page.waitForRequest((request) => request.url().includes("/admin/api/previews/7001/0?size=original"));
    await firstHistoryImage.click(); await historyViewerRequest;
    assert.equal(await page.locator(".pixiv-viewer-image").getAttribute("src"), "/admin/api/previews/7001/0?size=original");
    assert.equal(page.url(), base + "/admin/", "history viewer must open the fixed Curator endpoint, not navigate to Pixiv");
    await page.locator(".pixiv-viewer-close").click();
    await page.locator("#tab-stats").click();
    await page.locator(".stat-value").first().waitFor();
    assert.equal(await page.locator(".stat-value").first().textContent(), "1,234");
    await page.locator("#api-account").selectOption("test");
    await page.locator("#api-rating").selectOption("0");
    await page.locator("#api-ai").selectOption("0");
    await page.locator("#api-include-tags").fill("猫|狗\n风景");
    await page.locator("#api-exclude-manga").check();
    let generated = new URL(await page.locator("#api-url").inputValue());
    assert.equal(generated.searchParams.get("format"), "json");
    assert.equal(generated.searchParams.get("exclude_manga"), null);
    assert.equal(generated.searchParams.get("delivery"), null);
    assert.equal(generated.searchParams.get("r18"), "0");
    assert.equal(generated.searchParams.get("ai_type"), "0");
    assert.deepEqual(generated.searchParams.getAll("included_tags"), ["猫|狗", "风景"]);
    await page.locator("#api-exclude-manga").uncheck();
    assert.equal(new URL(await page.locator("#api-url").inputValue()).searchParams.get("exclude_manga"), "0");
    await page.locator("#api-exclude-manga").check();
    const pagesBeforeJSONTest = page.context().pages().length;
    await page.locator("#test-api").click();
    await page.waitForFunction(() => document.querySelector("#api-result").textContent.includes("i.pximg.net"));
    const jsonResult = JSON.parse(await page.locator("#api-result").textContent());
    assert.equal(jsonResult.illust_id, "100", "JSON mode should keep displaying the API response");
    assert.match(hits.findLast((u) => u.startsWith("/random?")), /format=json/);
    assert.equal(page.context().pages().length, pagesBeforeJSONTest, "JSON mode should not open a new tab");
    assert.equal(hits.some((u) => u === "/mock-image.png" || /\/random\?[^ ]*format=image/.test(u)), false, "JSON test should not request image bytes");
    await page.locator("#api-rating").selectOption("unknown");
    await page.locator("#api-ai").selectOption("unknown");
    let apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("r18"), "unknown");
    assert.equal(apiURL.searchParams.get("ai_type"), "unknown");
    await page.locator("#api-delivery").selectOption("original");
    apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("delivery"), "original");
    await page.locator("#api-format").selectOption("redirect");
    apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("format"), "redirect");
    const redirectPopupPromise = page.context().waitForEvent("page");
    await page.locator("#test-api").click();
    const redirectPopup = await redirectPopupPromise; await redirectPopup.waitForLoadState("load");
    assert.equal(new URL(redirectPopup.url()).pathname, "/mock-image.png", "302 mode should open and follow a same-origin image response");
    assert.ok(hits.some((hit) => hit.startsWith("/random?") && hit.includes("format=redirect")) && hits.includes("/mock-image.png"));
    await redirectPopup.close();
    await page.locator("#api-format").selectOption("image");
    apiURL = new URL(await page.locator("#api-url").inputValue());
    assert.equal(apiURL.searchParams.get("format"), "image");
    assert.equal(apiURL.searchParams.get("delivery"), null, "image format should use implicit proxy delivery");
    assert.equal(await page.locator('#api-delivery option[value="original"]').evaluate((option) => option.disabled), true);
    const imagePopupPromise = page.context().waitForEvent("page");
    await page.locator("#test-api").click();
    const imagePopup = await imagePopupPromise; await imagePopup.waitForLoadState("load");
    assert.equal(new URL(imagePopup.url()).searchParams.get("format"), "image", "image mode should open the actual image URL in a new tab");
    assert.ok(hits.some((hit) => hit.startsWith("/random?") && hit.includes("format=image")));
    await imagePopup.close();

    await page.locator("#tab-settings").click();
    await page.locator(".account-card[data-account-id='second']").waitFor();
    assert.equal(accountWrites.length, 0, "loading accounts with default-off options must not perform remote writes");
    assert.equal(await page.locator(".account-card[data-account-id='second'] [data-follow-on-approve]").isChecked(), false);
    const secondAccount = page.locator(".account-card[data-account-id='second']");
	const defaultRequest = page.waitForResponse((response) => response.request().method() === "PATCH" && response.url().endsWith("/admin/api/accounts/second"));
    await secondAccount.locator("[data-make-default]").click();
	const makeDefaultResponse = await defaultRequest;
	assert.deepEqual(makeDefaultResponse.request().postDataJSON(), { is_default: true }, "default account is set only after the explicit button click");
    await page.waitForFunction(() => document.querySelector(".account-card[data-account-id='second'] [data-make-default]")?.textContent === "默认账号");
	const followRequest = page.waitForResponse((response) => response.request().method() === "PATCH" && response.url().endsWith("/admin/api/accounts/second"));
    await page.locator(".account-card[data-account-id='second'] [data-follow-on-approve]").check();
	const followResponse = await followRequest;
	assert.deepEqual(followResponse.request().postDataJSON(), { follow_on_approve: true }, "follow-on-approve changes only after explicit checkbox action");
    const copyForm = page.locator("#copy-following-form");
    await copyForm.waitFor();
    const writesBeforeEmptyCopy = accountWrites.length;
    await copyForm.locator("button[type='submit']").click();
    assert.equal(accountWrites.length, writesBeforeEmptyCopy, "copy-following must wait for an explicit source account choice");
    await page.locator("#copy-following-source").selectOption("test");
    await page.locator("#copy-following-target").selectOption("test");
    await copyForm.locator("button[type='submit']").click();
    assert.equal(accountWrites.length, writesBeforeEmptyCopy, "copy-following rejects matching source and target accounts");
    await page.locator("#copy-following-target").selectOption("second");
	const copyRequest = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith("/admin/api/accounts/second/copy-following"));
	await copyForm.locator("button[type='submit']").click();
	const copyFollowingResponse = await copyRequest;
	assert.deepEqual(copyFollowingResponse.request().postDataJSON(), { source_account_id: "test" }, "copy-following sends only the selected source after explicit click");
    await page.locator('#preferences-form input[name="rating"][value="r18"]').check();
    await page.locator('#preferences-form input[name="rating"][value="unknown"]').check();
    await page.locator('#preferences-form input[name="ai"][value="unknown"]').check();
    await page.locator("#exclude-manga").check();
    await page.locator('#preferences-form button[type="submit"]').click();
    await page.waitForFunction(() => document.querySelector("#preference-version").textContent.includes("8"));
    assert.deepEqual(preferenceWrites.at(-1).rules.allowed_ratings, ["safe", "r18", "r18g", "unknown"]);
    assert.deepEqual(preferenceWrites.at(-1).rules.allowed_ai, ["non_ai", "unknown"]);
    assert.equal(preferenceWrites.at(-1).rules.exclude_manga, true);
    await page.reload();
    await page.locator("#tab-settings").click();
    await page.waitForFunction(() => document.querySelector('#preferences-form input[name="rating"][value="unknown"]').checked && document.querySelector('#preferences-form input[name="ai"][value="unknown"]').checked && document.querySelector("#exclude-manga").checked);
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
