(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const message = $("#global-message");
  let libraryPage = 1, libraryPages = 0, libraryLoading = false;
  let detailController = null, detailArtist = null, detailCursors = [""], detailPage = 0;
  let libraryGeneration = 0;
  let preferenceVersion = 0;
  let activeTab = "stats";
  let recommendationRunID = "";
  let recommendationTimer = null;
  let recommendationController = null;
  let recommendationPollError = false;
  let recommendationRenderedKey = "", recommendationPendingID = "";
  let tagController = null, tagSearchTimer = null;
  let accountTimer = null;
  let accountStatusLoading = false;
  let accountChoices = [];
  let historyPage = 1, historyPages = 0, historyController = null;
  let libraryReviewController = null;
  const historyFeedback = new Map(), historyFeedbackPending = new Set();

  function node(tag, className = "", text) {
    const item = document.createElement(tag);
    if (className) item.className = className;
    if (text !== undefined) item.textContent = String(text);
    return item;
  }
  function button(parent, label, style, handler) {
    const item = node("button", style, label);
    item.type = "button";
    item.addEventListener("click", handler);
    parent.append(item);
    return item;
  }
  function showMessage(text, error = false) {
    message.textContent = text;
    message.classList.toggle("is-error", error);
    message.hidden = !text;
  }
  function csrfToken() {
    const item = document.cookie.split(";").map((part) => part.trim()).find((part) => part.startsWith("curator_csrf="));
    return item ? decodeURIComponent(item.slice("curator_csrf=".length)) : "";
  }
  async function api(path, options = {}) {
    const method = options.method || "GET";
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    if (method !== "GET" && method !== "HEAD") {
      headers.set("Origin", window.location.origin);
      headers.set("X-CSRF-Token", csrfToken());
    }
    const response = await fetch(path, { ...options, method, headers, credentials: "same-origin" });
    let payload = {};
    try { payload = await response.json(); } catch (_) {}
    if (!response.ok) {
      const error = new Error(payload?.error?.code || `HTTP_${response.status}`);
      error.status = response.status;
      throw error;
    }
    return payload;
  }
  function report(error, context) {
    const labels = {
      UNAUTHORIZED: "登录已过期，请重新登录。", csrf_failed: "会话校验失败，请刷新页面后重试。",
      CONFLICT: "状态刚刚发生变化，请刷新后重试。", INVALID_INPUT: "填写内容不符合要求。",
      NOT_FOUND: "记录不存在或当前不可用。", NOT_READY: "服务暂时不可用，请稍后重试。",
      AUTH_REQUIRED: "Pixiv 凭据需要更新，请在设置中更换 Refresh Token。",
      NETWORK_ERROR: "连接 Pixiv 失败，请检查服务端代理或切换节点后重新提交。"
    };
    showMessage(labels[error.message] || `${context}失败，请稍后重试。`, true);
  }
  function setTab(tabName) {
    activeTab = tabName;
    $("#current-page-title").textContent = { library: "画师图库", discover: "发现画师", history: "分发历史", stats: "统计与接口", settings: "设置" }[tabName];
    $$(".tab").forEach((tab) => {
      const selected = tab.dataset.tab === tabName;
      tab.classList.toggle("is-active", selected);
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
    });
    $$(".tab-panel").forEach((panel) => { panel.hidden = panel.dataset.panel !== tabName; });
    if (tabName !== "discover") { stopRecommendationPolling(); recommendationController?.abort(); }
    if (tabName !== "settings") stopAccountPolling();
    if (tabName !== "history") historyController?.abort();
    if (tabName === "history") loadHistory();
    if (tabName === "library") { loadLibrary(); loadLibraryFilters(); }
    if (tabName === "stats") { loadStats(); loadAPIAccounts(); updateAPIURL(); }
    if (tabName === "discover") loadLatestRecommendations();
    if (tabName === "settings") { loadAccounts(); loadNetwork(); loadPreferences(); startAccountPolling(); }
  }
  const mobileNavigation = window.matchMedia("(max-width: 767px)");
  function updateNavigationOrientation() {
    $(".tabs").setAttribute("aria-orientation", mobileNavigation.matches ? "horizontal" : "vertical");
  }
  mobileNavigation.addEventListener("change", updateNavigationOrientation);
  updateNavigationOrientation();
  $$(".tab").forEach((tab, index, tabs) => {
    tab.addEventListener("click", () => setTab(tab.dataset.tab));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (["ArrowRight", "ArrowDown"].includes(event.key) ? 1 : tabs.length - 1)) % tabs.length;
      tabs[next].focus(); tabs[next].click();
    });
  });

  async function loadLibrary(reset = false) {
    if (reset) libraryPage = 1;
    const generation = ++libraryGeneration;
    libraryLoading = true; updateLibraryPager();
    const list = $("#artist-list");
    list.setAttribute("aria-busy", "true");
    list.replaceChildren(node("p", "empty-cell", "正在读取图库…"));
    try {
      const query = new URLSearchParams({ limit: $("#library-page-size").value, page: String(libraryPage) });
      const tag = $("#library-tag").value, account = $("#library-account").value, collection = $("#library-collection").value;
      if (tag) query.set("tag", tag);
      if (account) query.set("account_id", account);
      if (collection !== "") query.set("collection_enabled", collection);
      const result = await api("/admin/api/library/artists?" + query);
      if (generation !== libraryGeneration) return;
      libraryPages = Number(result.total_pages) || 0;
      if (!libraryPages) libraryPage = 1;
      if (libraryPages && libraryPage > libraryPages) { libraryPage = libraryPages; return loadLibrary(); }
      const artists = Array.isArray(result.items) ? result.items : [];
      list.replaceChildren();
      artists.forEach((artist) => list.append(renderArtist(artist)));
      $("#artist-count").textContent = String(result.library_total ?? result.total ?? 0);
      $("#library-summary").textContent = "符合条件 " + (result.total || 0) + " 位 · 本页 " + artists.length + " 位";
      if (!artists.length) {
        const empty = node("div", "empty-cell");
        empty.append(node("p", "", "暂无符合条件的画师。可以调整筛选、同步关注，或手动添加 UID。"));
        button(empty, "管理 Pixiv 账号", "button button-secondary button-small", () => setTab("settings"));
        list.append(empty);
      }
    } catch (error) {
      if (generation !== libraryGeneration) return;
      list.replaceChildren(node("p", "empty-cell", "图库暂时无法读取，请点击刷新重试。"));
      report(error, "读取图库");
    } finally {
      if (generation === libraryGeneration) {
        libraryLoading = false; list.setAttribute("aria-busy", "false"); updateLibraryPager();
      }
    }
  }
  function updateLibraryPager() {
    $("#previous-artists").disabled = libraryLoading || libraryPage <= 1;
    $("#next-artists").disabled = libraryLoading || libraryPage >= libraryPages;
    $("#artist-page-status").textContent = libraryLoading ? "正在读取…" : libraryPages ? "第 " + libraryPage + " / " + libraryPages + " 页" : "共 0 页";
    $("#library-page-number").value = String(libraryPage);
    $("#library-page-number").max = String(Math.max(1, libraryPages));
    $("#library-page-jump").querySelector("button").disabled = libraryLoading || !libraryPages;
  }
  async function loadTagOptions() {
    tagController?.abort(); const controller = new AbortController(); tagController = controller;
    const select = $("#library-tag"), selected = select.value;
    const query = new URLSearchParams({ limit: "30", q: $("#library-tag-search").value.trim() });
    try {
      const result = await api("/admin/api/library/tags?" + query, { signal: controller.signal });
      if (controller.signal.aborted) return;
      const tags = (Array.isArray(result.items) ? result.items : []).slice(0, 30);
      const options = document.createDocumentFragment(); options.append(new Option("所有标签", ""));
      if (selected && !tags.includes(selected)) options.append(new Option(selected, selected));
      tags.forEach((tag) => options.append(new Option(tag, tag)));
      select.replaceChildren(options); select.value = selected;
      $("#library-tag-hint").textContent = tags.length ? "选择标签应用筛选；输入关键词查找更多。" : "没有匹配标签，可换一个关键词。";
    } catch (error) { if (!controller.signal.aborted) $("#library-tag-hint").textContent = "标签暂不可用，仍可浏览图库。"; }
  }
  $("#library-tag-search").addEventListener("input", () => { clearTimeout(tagSearchTimer); tagSearchTimer = setTimeout(loadTagOptions, 200); });
  async function loadLibraryFilters() {
    loadTagOptions();
    try {
      const result = await api("/admin/api/accounts");
      const select = $("#library-account"), selected = select.value;
      const accounts = Array.isArray(result.items) ? result.items : [];
      const options = document.createDocumentFragment(); options.append(new Option("所有账号", ""));
      accounts.forEach((account) => options.append(new Option(account.name || account.id, account.id)));
      select.replaceChildren(options);
      if (accounts.some((account) => String(account.id) === selected)) select.value = selected;
    } catch (_) { /* Library remains usable without account filters. */ }
  }
  function renderArtist(artist) {
    const id = String(artist.id || "");
    const card = node("article", `artist-card${artist.review_status === "blocked" ? " is-blocked" : ""}`);
    card.dataset.artistId = id;
    const body = node("div", "artist-card-body");
    const title = node("div", "artist-title-row");
    const name = node("h3", "artist-name");
    button(name, artist.name || "Pixiv UID " + id, "artist-detail-link", () => openArtistDetails(artist));
    title.append(name);
    title.append(node("span", `status-pill ${artist.review_status || "pending"}${artist.review_status === "approved" && !artist.collection_enabled ? " is-paused" : ""}`, artist.review_status === "approved" && !artist.collection_enabled ? "已暂停" : statusText(artist.review_status)));
    body.append(title, node("p", "artist-id", `UID ${id}${artist.account ? ` · ${artist.account}` : ""}`));
    const tags = Array.isArray(artist.tags) ? artist.tags : [];
    const tagList = node("div", "tag-list");
    if (tags.length) tags.slice(0, 10).forEach((tag) => tagList.append(node("span", "tag-chip", tag)));
    else tagList.append(node("span", "muted", "暂无标签"));
    body.append(tagList);
    const edit = node("details", "tag-editor");
    const summary = node("summary", "button button-quiet button-small", "修正标签");
    const editor = node("input"); editor.type = "text"; editor.value = tags.join(", "); editor.setAttribute("aria-label", `${artist.name || id} 的画师标签，以逗号分隔`); editor.placeholder = "标签一, 标签二";
    const save = node("button", "button button-secondary button-small", "保存标签"); save.type = "button";
    save.addEventListener("click", async () => {
      if (save.disabled) return;
      save.disabled = true;
      try {
        const next = [...new Set(editor.value.split(",").map((value) => value.trim()).filter(Boolean))];
        await api(`/admin/api/library/artists/${encodeURIComponent(id)}/tags`, { method: "PUT", body: JSON.stringify({ tags: next }) });
        showMessage("画师标签已更新。"); await loadLibrary();
      } catch (error) { report(error, "保存标签"); }
      finally { save.disabled = false; }
    });
    edit.append(summary, editor, save);
    const actions = node("div", "artist-actions");
    actions.append(edit);
    button(actions, "查看详情", "button button-primary button-small", () => openArtistDetails(artist));
    if (artist.review_status === "approved") {
      button(actions, "屏蔽画师", "button button-danger button-small", (event) => libraryArtistAction(id, "block", event.currentTarget));
    } else if (artist.review_status === "blocked") {
      button(actions, "恢复到图库", "button button-secondary button-small", (event) => libraryArtistAction(id, "unblock", event.currentTarget));
    } else {
      button(actions, "恢复并加入图库", "button button-secondary button-small", (event) => restoreArtistToLibrary(id, event.currentTarget));
    }
    if (artist.collection_enabled) button(actions, "暂停采集", "button button-quiet button-small", (event) => toggleCollection(id, false, event.currentTarget));
    else if (artist.review_status === "approved") button(actions, "恢复采集", "button button-quiet button-small", (event) => toggleCollection(id, true, event.currentTarget));
    body.append(actions); card.append(body); return card;
  }
  function appendArtworkPreview(container, artwork) {
    const pages = Array.isArray(artwork.pages) ? artwork.pages : [];
    const page = pages.find((item) => item.media_state === "indexed") || pages[0];
    if (!page || Number(page.page_index) < 0) { container.append(node("span", "cover-empty", "暂无可用预览")); return; }
    const image = node("img", "cover-image"); image.loading = "lazy"; image.decoding = "async"; image.alt = artwork.title || "画师代表作";
    image.src = `/admin/api/previews/${encodeURIComponent(String(artwork.id))}/${encodeURIComponent(String(page.page_index))}`;
    image.addEventListener("error", () => { image.remove(); container.append(node("span", "cover-empty", "预览暂不可用")); }, { once: true });
    container.append(image);
  }
  function statusText(status) { return ({ approved: "已收录", pending: "正在加入", blocked: "已屏蔽", rejected: "未收录" })[status] || "已收录"; }
  async function loadLibraryReview() {
    libraryReviewController?.abort(); const controller = new AbortController(); libraryReviewController = controller;
    const list = $("#library-review-list"); list.setAttribute("aria-busy", "true");
    list.replaceChildren(node("p", "empty-cell", "正在读取…")); $("#reload-library-review").disabled = true;
    try {
      const result = await api("/admin/api/library/review", { signal: controller.signal });
      if (controller.signal.aborted || !$("#library-review").open) return;
      const fragment = document.createDocumentFragment();
      (Array.isArray(result.items) ? result.items : []).slice(0, 5).forEach((item) => {
        const artist = item.artist || {}, id = String(artist.id || "");
        const card = node("article", "library-review-card"); card.dataset.artistId = id;
        const heading = node("div", "library-review-heading");
        const identity = node("div"); identity.append(node("h3", "artist-name", artist.name || "Pixiv UID " + id), node("p", "artist-id", "UID " + id)); heading.append(identity);
        button(heading, "保留", "button button-quiet button-small", (event) => { event.currentTarget.closest(".library-review-card")?.remove(); if (!list.querySelector(".library-review-card")) list.replaceChildren(node("p", "empty-cell", "本组已整理完。")); });
        card.append(heading);
        const samples = node("div", "library-review-samples");
        (Array.isArray(item.sample_illust_ids) ? item.sample_illust_ids : []).slice(0, 3).forEach((illustID) => {
          const tile = node("button", "sample-tile"); tile.type = "button"; tile.setAttribute("aria-label", "查看作品 " + illustID + " 大图");
          tile.addEventListener("click", () => window.PixivCuratorViewer.open({ id: illustID, artistId: id, pageIndex: 0, title: "代表作 " + illustID, artistName: artist.name || "" }));
          const image = node("img", "cover-image"); image.loading = "lazy"; image.decoding = "async"; image.alt = "代表作 " + illustID; image.src = `/admin/api/previews/${encodeURIComponent(String(illustID))}/0`; tile.append(image); samples.append(tile);
        });
        if (samples.children.length) card.append(samples);
        const actions = node("div", "library-review-actions");
        actions.append(node("p", "field-help", "移出只从图库隐藏，保留本地目录，也不会取关。"));
        button(actions, "移出图库", "button button-danger button-small", async (event) => {
          const trigger = event.currentTarget; if (trigger.disabled) return; trigger.disabled = true;
          try { await api(`/admin/api/library/artists/${encodeURIComponent(id)}/remove`, { method: "POST" }); card.remove(); showMessage("画师已移出图库；本地目录和关注关系保留。"); await loadLibrary(); if (!list.querySelector(".library-review-card")) list.replaceChildren(node("p", "empty-cell", "本组已整理完。")); }
          catch (error) { report(error, "移出图库"); trigger.disabled = false; }
        });
        card.append(actions); fragment.append(card);
      });
      if (!fragment.children.length) fragment.append(node("p", "empty-cell", "目前没有可整理的画师。"));
      list.replaceChildren(fragment);
    } catch (error) { if (!controller.signal.aborted) { report(error, "读取整理建议"); list.replaceChildren(node("p", "empty-cell", "暂时无法读取，稍后可换一组重试。")); } }
    finally { if (!controller.signal.aborted) { list.setAttribute("aria-busy", "false"); $("#reload-library-review").disabled = false; } }
  }
  $("#library-review").addEventListener("toggle", () => { if ($("#library-review").open) loadLibraryReview(); else libraryReviewController?.abort(); });
  $("#reload-library-review").addEventListener("click", loadLibraryReview);
  async function libraryArtistAction(id, action, trigger) {
    if (trigger?.disabled) return;
    if (trigger) trigger.disabled = true;
    try {
      await api(`/admin/api/artists/${encodeURIComponent(id)}/${action}`, { method: "POST" });
      const messages = { approve: "画师已加入图库，后台会自动安排采集。", block: "画师已屏蔽，后续不会供图。", unblock: "画师已恢复到图库。", restore: "画师已恢复。" };
      showMessage(messages[action] || "画师状态已更新。"); await loadLibrary();
    } catch (error) { report(error, "更新画师状态"); if (trigger) trigger.disabled = false; }
  }
  async function restoreArtistToLibrary(id, trigger) {
    if (trigger?.disabled) return;
    trigger.disabled = true;
    try {
      await api(`/admin/api/artists/${encodeURIComponent(id)}/restore`, { method: "POST" });
      await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" });
      showMessage("画师已恢复并加入图库。"); await loadLibrary();
    } catch (error) { report(error, "恢复画师"); trigger.disabled = false; }
  }
  async function toggleCollection(id, enabled, trigger) {
    if (trigger?.disabled) return;
    if (trigger) trigger.disabled = true;
    try { await api(`/admin/api/artists/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ collection_enabled: enabled }) }); showMessage(enabled ? "已恢复后续采集。" : "已暂停后续采集，已入库作品仍保留。"); await loadLibrary(); }
    catch (error) { report(error, "更新采集状态"); if (trigger) trigger.disabled = false; }
  }
  $("#manual-artist-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const input = $("#manual-artist-id"), id = input.value.trim();
    const submit = event.currentTarget.querySelector("button[type=submit]");
    if (submit.disabled) return;
    submit.disabled = true;
    try {
      await api("/admin/api/artists", { method: "POST", body: JSON.stringify({ id }) });
      await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" });
      input.value = ""; showMessage(`UID ${id} 已加入图库，后台会自动安排采集。`); await loadLibrary();
    } catch (error) { report(error, "添加画师"); }
    finally { submit.disabled = false; }
  });
  $("#reload-library").addEventListener("click", () => { loadLibrary(); loadLibraryFilters(); });
  $("#previous-artists").addEventListener("click", () => { if (!libraryLoading && libraryPage > 1) { libraryPage--; loadLibrary(); } });
  $("#next-artists").addEventListener("click", () => { if (!libraryLoading && libraryPage < libraryPages) { libraryPage++; loadLibrary(); } });
  $("#library-page-jump").addEventListener("submit", (event) => {
    event.preventDefault(); const page = Number($("#library-page-number").value);
    if (!libraryLoading && Number.isInteger(page) && page >= 1 && page <= libraryPages) { libraryPage = page; loadLibrary(); }
  });
  $("#library-page-size").addEventListener("change", () => loadLibrary(true));
  $("#library-tag").addEventListener("change", () => loadLibrary(true));
  $("#library-account").addEventListener("change", () => loadLibrary(true));
  $("#library-collection").addEventListener("change", () => loadLibrary(true));

  function recommendationQuery(runID = "") {
    const query = new URLSearchParams({ content_rating: $("#recommendation-rating").value, ai_type: $("#recommendation-ai").value });
    if (runID) query.set("run_id", runID);
    return query;
  }
  function updateRecommendationStatus(run, loadingSamples = false) {
    const status = $("#recommendation-status");
    const progress = Math.max(0, Math.min(100, Number(run.progress_percent) || 0));
    const displayProgress = loadingSamples && progress >= 100 ? 99 : progress;
    const key = [run.id, run.state, progress, loadingSamples].join(":");
    if (status.dataset.stateKey === key) return;
    status.dataset.stateKey = key;
    const parts = document.createDocumentFragment();
    parts.append(node("span", "run-state", loadingSamples ? "推荐已生成，正在加载代表作" : "为你发现的画师"));
    parts.append(node("span", `run-pill state-${run.state || "unknown"}`, loadingSamples ? "预览准备中" : runLabel(run.state)));
    parts.append(node("span", "run-meta", `${displayProgress}%`));
    status.replaceChildren(parts);
    const running = ["pending", "running", "retry_wait"].includes(run.state);
    $("#start-recommendations").disabled = running;
    $("#start-recommendations").textContent = running ? "正在发现…" : "换一批";
  }
  async function prepareRecommendationCards(items, signal) {
    const fragment = document.createDocumentFragment();
    const oldCards = new Map($$("#recommendation-list .recommendation-card[data-artist-id]").map((card) => [card.dataset.artistId, card]));
    const cards = items.filter((item) => Array.isArray(item.sample_illust_ids) && item.sample_illust_ids.length).slice(0, 30).map((item) => {
      const id = String(item.artist_id || item.artist?.id || "");
      if (item.components?.carryover && oldCards.has(id)) { const stub = node("article"); stub.dataset.reuseArtist = id; return stub; }
      return renderRecommendation(item);
    });
    cards.forEach((card) => fragment.append(card));
    const images = Array.from(fragment.querySelectorAll("img[data-preview-src]")).sort((a, b) => Number(a.dataset.sampleIndex || 0) - Number(b.dataset.sampleIndex || 0));
    let index = 0; const deadline = Date.now() + 15000;
    async function next() {
      while (!signal.aborted && index < images.length) {
        const image = images[index++];
        if (Date.now() >= deadline) { image.replaceWith(node("span", "cover-empty", "预览暂不可用")); continue; }
        await new Promise((resolve) => {
          let done = false, timer;
          const finish = () => { if (done) return; done = true; clearTimeout(timer); image.removeEventListener("load", loaded); image.removeEventListener("error", fail); signal.removeEventListener("abort", cancel); resolve(); };
          const fail = () => { image.removeAttribute("src"); image.replaceWith(node("span", "cover-empty", "预览暂不可用")); finish(); };
          const cancel = () => { image.removeAttribute("src"); finish(); };
          const loaded = () => { if (typeof image.decode === "function") image.decode().catch(() => {}).then(finish); else finish(); };
          image.addEventListener("load", loaded, { once: true }); image.addEventListener("error", fail, { once: true }); signal.addEventListener("abort", cancel, { once: true });
          timer = setTimeout(fail, Math.max(1, Math.min(5000, deadline - Date.now())));
          image.loading = "eager"; image.src = image.dataset.previewSrc; delete image.dataset.previewSrc;
        });
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, images.length) }, next));
    if (signal.aborted) { images.forEach((image) => image.removeAttribute("src")); return null; }
    fragment.querySelectorAll("[data-reuse-artist]").forEach((stub) => { const old = oldCards.get(stub.dataset.reuseArtist); if (old?.isConnected) stub.replaceWith(old); else stub.remove(); });
    cards.forEach((card) => {
      const approval = card.querySelector("[data-approve]");
      if (approval) { approval.disabled = !Array.from(card.querySelectorAll("img")).some((image) => image.complete && image.naturalWidth > 0); if (approval.disabled) approval.title = "至少加载一张代表作后才可加入"; }
    });
    return fragment;
  }
  async function readRecommendations(runID = "", invalidate = false) {
    stopRecommendationPolling(); recommendationController?.abort();
    const controller = new AbortController(); recommendationController = controller;
    const list = $("#recommendation-list");
    if (invalidate) { recommendationRenderedKey = ""; list.replaceChildren(node("p", "empty-cell", "正在读取推荐…")); }
    try {
      const path = runID ? "/admin/api/recommendations?" : "/admin/api/recommendation-runs/latest?";
      const result = await api(path + new URLSearchParams(runID ? { run_id: runID } : {}), { signal: controller.signal });
      if (controller.signal.aborted || activeTab !== "discover") return;
      const run = result.run || {};
      recommendationRunID = String(run.id || runID || "");
      if (!recommendationRunID) throw new Error("NOT_FOUND");
      if (recommendationPollError) { recommendationPollError = false; showMessage(""); }
      updateRecommendationStatus(run);
      if (["pending", "running", "retry_wait"].includes(run.state)) {
        if (!list.querySelector(".recommendation-card") && (recommendationPendingID !== recommendationRunID || !list.children.length || invalidate)) {
          list.replaceChildren(node("p", "empty-cell", "正在收集候选与代表作，完成后一次展示；候选不会自动加入图库。"));
          recommendationPendingID = recommendationRunID; recommendationRenderedKey = "";
        }
        if (!list.querySelector(".recommendation-card")) {
          const carried = (result.items || []).filter((item) => item.components?.carryover && ($("#recommendation-rating").value === "all" || item.content_rating === $("#recommendation-rating").value) && ($("#recommendation-ai").value === "all" || item.ai_status === $("#recommendation-ai").value));
          if (carried.length) { const carriedFragment = await prepareRecommendationCards(carried, controller.signal); if (carriedFragment && !controller.signal.aborted) list.replaceChildren(carriedFragment); }
        }
        startRecommendationPolling(); return;
      }
      recommendationPendingID = "";
      if (run.state !== "succeeded") {
        list.replaceChildren(node("p", "empty-cell", run.state === "paused_auth" ? "请先在设置中检查账号，恢复任务后再查看。" : "本轮发现未完成，可以稍后重新开始。")); return;
      }
      const rating = $("#recommendation-rating").value, ai = $("#recommendation-ai").value;
      const items = (Array.isArray(result.items) ? result.items : []).filter((item) => (rating === "all" || item.content_rating === rating) && (ai === "all" || item.ai_status === ai));
      const key = JSON.stringify([run.id, recommendationQuery().toString(), items]);
      if (key === recommendationRenderedKey) { startRecommendationPolling(30000); return; }
      updateRecommendationStatus(run, true);
      if (!list.querySelector(".recommendation-card")) list.replaceChildren(node("p", "empty-cell", "正在加载代表作，准备完成后统一显示…"));
      const fragment = await prepareRecommendationCards(items, controller.signal);
      if (!fragment || controller.signal.aborted || activeTab !== "discover") return;
      if (!fragment.children.length) fragment.append(node("p", "empty-cell", "当前筛选下没有带代表作的候选，可调整筛选或生成下一批。"));
      list.replaceChildren(fragment); recommendationRenderedKey = key; updateRecommendationStatus(run); startRecommendationPolling(30000);
    } catch (error) {
      if (controller.signal.aborted || activeTab !== "discover") return;
      $("#start-recommendations").disabled = false; $("#start-recommendations").textContent = "开始发现";
      if (error.message === "NOT_FOUND") {
        $("#recommendation-status").replaceChildren(node("span", "run-state", "还没有推荐记录"));
        list.replaceChildren(node("p", "empty-cell", "点击“开始发现”，生成第一批候选画师。"));
      } else { recommendationPollError = true; report(error, "读取推荐"); if (!list.querySelector(".recommendation-card")) list.replaceChildren(node("p", "empty-cell", "推荐暂时无法读取。")); }
    }
  }
  function loadLatestRecommendations() { return readRecommendations(); }
  function loadRecommendationByID(runID) { return readRecommendations(runID); }
  $("#recommendation-rating").addEventListener("change", () => readRecommendations("", true));
  $("#recommendation-ai").addEventListener("change", () => readRecommendations("", true));
  $("#start-recommendations").addEventListener("click", async (event) => {
    const trigger = event.currentTarget; if (trigger.disabled) return;
    stopRecommendationPolling(); recommendationController?.abort();
    trigger.disabled = true; trigger.textContent = "正在提交…";
    try {
      const result = await api("/admin/api/recommendations", { method: "POST" });
      recommendationRunID = String(result.run_id || "");
      showMessage("已提交发现任务，生成完整候选后再按当前筛选展示。");
      await readRecommendations(recommendationRunID, true);
    } catch (error) { trigger.disabled = false; trigger.textContent = "开始发现"; report(error, "开始发现"); }
  });
  function startRecommendationPolling(delay = 2500) {
    stopRecommendationPolling();
    if (activeTab === "discover" && document.visibilityState === "visible") recommendationTimer = window.setTimeout(loadLatestRecommendations, delay);
  }
  function stopRecommendationPolling() { if (recommendationTimer !== null) { window.clearTimeout(recommendationTimer); recommendationTimer = null; } }
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState !== "visible") { stopRecommendationPolling(); stopAccountPolling(); }
    else { if (activeTab === "discover") loadLatestRecommendations(); if (activeTab === "settings") { refreshAccountStatuses(); startAccountPolling(); } }
  });
  let lastDiscoveryActivity = 0, discoveryActivityTimer = null;
  function sendDiscoveryActivity() { lastDiscoveryActivity = Date.now(); api("/admin/api/recommendations/activity", { method: "POST", body: "{}" }).catch(() => {}); }
  function touchDiscoveryActivity() {
    if (activeTab !== "discover" || document.visibilityState !== "visible") return;
    clearTimeout(discoveryActivityTimer);
    if (Date.now() - lastDiscoveryActivity >= 30000) sendDiscoveryActivity();
    discoveryActivityTimer = setTimeout(sendDiscoveryActivity, 1000);
  }
  for (const event of ["pointerdown", "keydown", "scroll"]) document.addEventListener(event, touchDiscoveryActivity, { passive: true, capture: true });
  function runLabel(state) { return ({ pending: "排队中", running: "正在发现", retry_wait: "稍后重试", succeeded: "已完成", failed: "未完成", paused_auth: "需要检查账号" })[state] || "状态更新中"; }
  function renderRecommendation(item) {
    const artist = item.artist || {}, id = String(artist.id || item.artist_id || "");
    const card = node("article", "recommendation-card");
    card.dataset.artistId = id;
    const sampleIDs = Array.isArray(item.sample_illust_ids) ? item.sample_illust_ids.slice(0, 3) : [];
    const title = node("h3", "candidate-name");
    if (sampleIDs.length) {
      const titleButton = node("button", "candidate-title-button", artist.name || "Pixiv UID " + id);
      titleButton.type = "button";
      titleButton.setAttribute("aria-label", "查看 " + (artist.name || "Pixiv UID " + id) + " 的代表作大图");
      titleButton.addEventListener("click", () => window.PixivCuratorViewer.open({ id: sampleIDs[0], artistId: artist.id, pageIndex: 0, title: "代表作 " + sampleIDs[0], artistName: artist.name || "" }));
      title.append(titleButton);
    } else title.textContent = artist.name || "Pixiv UID " + id;
    const samples = node("div", "candidate-samples");
    const content = node("div", "recommendation-content");
    const heading = node("div", "candidate-heading");
    const identity = node("div"); identity.append(title, node("p", "artist-id", `UID ${id}${artist.account ? ` · ${artist.account}` : ""}`)); heading.append(identity);
    const score = Number(item.components?.display_score ?? item.score);
    const scoreBadge = node("span", "score-badge", Number.isFinite(score) ? `推荐指数 ${score.toFixed(2)}` : "推荐候选");
    scoreBadge.title = "仅用于候选排序参考，不代表喜欢概率"; heading.append(scoreBadge); content.append(heading);
    const tags = [item.tags, artist.tags, item.components?.matched_tags].find((value) => Array.isArray(value) && value.length) || [];
    const chips = node("div", "reason-list");
    if (tags.length) tags.slice(0, 8).forEach((tag) => chips.append(node("span", "reason-chip", tag)));
    else chips.append(node("span", "reason-chip", "暂无标签"));
    content.append(chips);
    const classification = node("p", "sample-classification");
    classification.append(node("span", "tag-chip", ({ adult: "含 R18 样本", safe: "样本均为全年龄", unknown: "分级未知" })[item.content_rating] || "分级未知"));
    classification.append(node("span", "tag-chip", ({ ai: "含 AI 样本", non_ai: "样本均为非 AI", unknown: "AI 状态未知" })[item.ai_status] || "AI 状态未知"));
    content.append(classification);
    if (sampleIDs.length) {
      for (const [sampleIndex, workID] of sampleIDs.entries()) {
        const tile = node("button", "sample-tile"); tile.type = "button"; tile.setAttribute("aria-label", "查看作品 " + workID + " 大图");
        tile.addEventListener("click", () => window.PixivCuratorViewer.open({ id: workID, artistId: artist.id, pageIndex: 0, title: "代表作 " + workID, artistName: artist.name || "" }));
        const image = node("img", "cover-image"); image.alt = "代表作 " + workID; image.decoding = "async";
        image.dataset.sampleIndex = String(sampleIndex); image.dataset.previewSrc = `/admin/api/previews/${encodeURIComponent(workID)}/0`; tile.append(image);
        tile.append(node("span", "sample-caption", "作品 " + workID));
        samples.append(tile);
      }
      content.prepend(samples);
    } else content.append(node("p", "field-help no-samples", "尚无代表作样本，分类保持未知。"));
    const actions = node("div", "candidate-actions");
    const exploring = !!item.exploration;
    let decisionPending = false;
    const approveButton = button(actions, "加入图库", "button button-primary", async (event) => {
      const actionButton = event.currentTarget;
      if (decisionPending) return;
      decisionPending = true; approveButton.disabled = true; rejectButton.disabled = true;
      try {
        await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" });
        showMessage("已加入图库，后台会自动安排采集。");
        if (exploring && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
          emitExplorationApprovalParticles(card);
          setTimeout(() => card.remove(), 475);
        } else card.remove();
      } catch (error) {
        decisionPending = false; actionButton.disabled = false; rejectButton.disabled = false;
        report(error, "加入图库");
      }
    });
    approveButton.dataset.approve = "true"; approveButton.disabled = true;
    const rejectButton = button(actions, "不感兴趣", "button button-quiet", async (event) => {
      const actionButton = event.currentTarget;
      if (decisionPending) return;
      decisionPending = true; approveButton.disabled = true; rejectButton.disabled = true;
      try { await api(`/admin/api/artists/${encodeURIComponent(id)}/reject`, { method: "POST" }); showMessage("已记录为不感兴趣。"); card.remove(); }
      catch (error) { decisionPending = false; actionButton.disabled = false; approveButton.disabled = false; report(error, "记录选择"); }
    });
    content.append(actions); card.append(content); return card;
  }
  function emitExplorationApprovalParticles(card) {
    const bounds = card.getBoundingClientRect();
    const zoom = Math.max(0.01, Number.parseFloat(getComputedStyle(document.body).zoom) || 1);
    const overlay = node("div", "exploration-particle-overlay");
    overlay.setAttribute("aria-hidden", "true");
    Object.assign(overlay.style, { left: `${bounds.left / zoom}px`, top: `${bounds.top / zoom}px`, width: `${bounds.width / zoom}px`, height: `${bounds.height / zoom}px` });
    for (let index = 0; index < 96; index += 1) {
      const particle = node("span", "exploration-particle");
      const x = ((index % 12) + 0.5 + (Math.random() - 0.5) * 0.35) / 12;
      const y = (Math.floor(index / 12) + 0.5 + (Math.random() - 0.5) * 0.35) / 8;
      const angle = Math.atan2(y - 0.5, x - 0.5);
      const distance = 46 + Math.random() * 48;
      particle.style.left = `${x * 100}%`;
      particle.style.top = `${y * 100}%`;
      particle.style.setProperty("--particle-x", `${Math.cos(angle) * distance}px`);
      particle.style.setProperty("--particle-y", `${Math.sin(angle) * distance}px`);
      particle.style.setProperty("--particle-delay", `${Math.random() * 50}ms`);
      overlay.append(particle);
    }
    document.body.append(overlay);
    setTimeout(() => overlay.remove(), 500);
  }
  function closeArtistDetails() {
    detailController?.abort(); detailController = null;
    $("#artwork-list").replaceChildren(); detailArtist = null;
  }
  async function openArtistDetails(artist) {
    closeArtistDetails(); detailArtist = artist; detailCursors = [""]; detailPage = 0;
    $("#artwork-heading").textContent = artist.name || "Pixiv UID " + artist.id;
    const info = $("#artist-detail-info"); info.replaceChildren();
    info.append(node("p", "muted", "UID " + artist.id + (artist.account ? " · " + artist.account : "")));
    info.append(node("p", "muted", statusText(artist.review_status) + " · " + (artist.collection_enabled ? "采集已启用" : "采集已暂停") + " · " + (artist.remote_followed ? "Pixiv 关注中" : "历史或本地收录")));
    const tags = node("div", "tag-list");
    (artist.tags || []).forEach((tag) => tags.append(node("span", "tag-chip", tag))); info.append(tags);
    const link = node("a", "button button-secondary button-small", "在 Pixiv 查看画师");
    link.href = "https://www.pixiv.net/users/" + encodeURIComponent(artist.id);
    link.target = "_blank"; link.rel = "noopener noreferrer"; info.append(link);
    $("#artwork-dialog").showModal(); await loadArtistDetails();
  }
  async function loadArtistDetails() {
    detailController?.abort(); const controller = new AbortController(); detailController = controller;
    const list = $("#artwork-list"); list.replaceChildren(node("p", "empty-cell", "正在读取画师作品…"));
    $("#previous-artworks").disabled = true; $("#next-artworks").disabled = true;
    $("#artwork-page-status").textContent = "作品第 " + (detailPage + 1) + " 页";
    try {
      const query = new URLSearchParams({ artist_id: detailArtist.id, limit: "8" });
      if (detailCursors[detailPage]) query.set("cursor", detailCursors[detailPage]);
      const result = await api("/admin/api/artworks?" + query, { signal: controller.signal });
      if (controller.signal.aborted) return;
      list.replaceChildren();
      for (const artwork of result.items || []) {
        const card = node("article", "artwork-card"), cover = node("div", "artist-cover");
        if (artwork.visible) {
          appendArtworkPreview(cover, artwork); cover.classList.add("image-open-target"); cover.tabIndex = 0; cover.setAttribute("role", "button"); cover.setAttribute("aria-label", "查看作品大图");
          const open = () => window.PixivCuratorViewer.open({ id: artwork.id, artistId: artwork.artist_id || detailArtist?.id, pageIndex: (artwork.pages || [])[0]?.page_index || 0, title: artwork.title, artistName: detailArtist?.name || "", pageCount: artwork.page_count, rating: artwork.rating, aiStatus: artwork.ai_status, tags: artwork.tags });
          cover.addEventListener("click", open); cover.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(); } });
        }
        else cover.append(node("span", "cover-empty", "作品当前不可见"));
        const info = node("div", "artwork-info");
        const title = node("div", "artwork-title-row");
        if (artwork.visible) {
          const titleButton = node("button", "artwork-title artwork-title-button", artwork.title || "作品 " + artwork.id);
          titleButton.type = "button"; titleButton.setAttribute("aria-label", "查看作品大图：" + (artwork.title || artwork.id));
          titleButton.addEventListener("click", () => window.PixivCuratorViewer.open({ id: artwork.id, artistId: artwork.artist_id || detailArtist?.id, pageIndex: (artwork.pages || [])[0]?.page_index || 0, title: artwork.title, artistName: detailArtist?.name || "", pageCount: artwork.page_count, rating: artwork.rating, aiStatus: artwork.ai_status, tags: artwork.tags }));
          title.append(titleButton);
        } else title.append(node("span", "artwork-title", artwork.title || "作品 " + artwork.id));
        const pixivLink = node("a", "artwork-pixiv-link", "Pixiv");
        pixivLink.href = "https://www.pixiv.net/artworks/" + encodeURIComponent(artwork.id);
        pixivLink.target = "_blank"; pixivLink.rel = "noopener noreferrer";
        const rating = { safe: "全年龄", r18: "R18", r18g: "R18" }[artwork.rating] || "分级未知";
        const ai = { non_ai: "非 AI", ai: "AI" }[artwork.ai_status] || "AI 状态未知";
        info.append(title, node("p", "artwork-meta", "ID " + artwork.id + " · " + artwork.page_count + " 页 · " + rating + " · " + ai), pixivLink);
        info.append(node("p", "artwork-tags", (artwork.tags || []).join(" · ") || "暂无作品标签"));
        card.append(cover, info); list.append(card);
      }
      if (!list.children.length) list.append(node("p", "empty-cell", "本页没有已收录作品。后台采集完成后可重新打开详情。"));
      detailCursors[detailPage + 1] = result.next_cursor || "";
      $("#previous-artworks").disabled = detailPage === 0;
      $("#next-artworks").disabled = !result.next_cursor;
    } catch (error) {
      if (controller.signal.aborted) return;
      list.replaceChildren(node("p", "empty-cell", "作品读取失败，请重试。"));
      button(list, "重新读取", "button button-secondary", loadArtistDetails); report(error, "读取画师详情");
    }
  }
  $("#previous-artworks").addEventListener("click", () => { detailPage--; loadArtistDetails(); });
  $("#next-artworks").addEventListener("click", () => { detailPage++; loadArtistDetails(); });
  $("#close-artworks").addEventListener("click", () => { closeArtistDetails(); $("#artwork-dialog").close(); });
  $("#artwork-dialog").addEventListener("click", (event) => {
    const dialog = event.currentTarget;
    if (event.target !== dialog) return;
    const bounds = dialog.getBoundingClientRect();
    if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) { closeArtistDetails(); dialog.close(); }
  });
  $("#artwork-dialog").addEventListener("cancel", closeArtistDetails);
  $("#artwork-dialog").addEventListener("close", () => { if (!$("#artwork-dialog").open) closeArtistDetails(); });

  async function loadHistory() {
    historyController?.abort(); const controller = new AbortController(); historyController = controller;
    const list = $("#history-list"); list.setAttribute("aria-busy", "true");
    $("#previous-history").disabled = true; $("#next-history").disabled = true;
    try {
      const result = await api("/admin/api/history?" + new URLSearchParams({ page: String(historyPage), limit: "24" }), { signal: controller.signal });
      if (controller.signal.aborted || activeTab !== "history") return;
      historyPages = Number(result.total_pages) || 0;
      if (historyPages && historyPage > historyPages) { historyPage = historyPages; return loadHistory(); }
      const fragment = document.createDocumentFragment();
      for (const item of result.items || []) {
        const card = node("article", "history-card"); card.dataset.historyId = String(item.id);
        const cover = node("button", "history-preview"); cover.type = "button"; cover.disabled = !item.available;
        if (item.available) {
          const image = node("img"); image.alt = item.title || "历史图片"; image.loading = "lazy"; image.decoding = "async";
          image.src = `/admin/api/previews/${encodeURIComponent(item.illust_id)}/${Number(item.page_index)}`;
          image.addEventListener("error", () => image.replaceWith(node("span", "cover-empty", "图片暂不可用")), { once: true }); cover.append(image);
          cover.addEventListener("click", () => window.PixivCuratorViewer.open({ id: item.illust_id, artistId: item.artist_id, pageIndex: item.page_index, title: item.title, artistName: item.artist_name, rating: item.rating, aiStatus: item.ai_status, tags: item.tags }));
        } else cover.append(node("span", "cover-empty", "当前不可预览"));
        const title = node("h3", "artwork-title");
        if (item.available) {
          const titleButton = node("button", "artwork-title-button", item.title || "作品 " + item.illust_id);
          titleButton.type = "button"; titleButton.setAttribute("aria-label", "查看作品大图：" + (item.title || item.illust_id));
          titleButton.addEventListener("click", () => window.PixivCuratorViewer.open({ id: item.illust_id, artistId: item.artist_id, pageIndex: item.page_index, title: item.title, artistName: item.artist_name, rating: item.rating, aiStatus: item.ai_status, tags: item.tags }));
          title.append(titleButton);
        } else title.textContent = item.title || "作品 " + item.illust_id;
        card.append(cover, title, node("p", "artwork-meta", (item.artist_name || "UID " + item.artist_id) + " · 第 " + (Number(item.page_index) + 1) + " 页"), node("p", "artwork-meta", formatTime(item.selected_at)));
        const key = `${item.illust_id}:${Number(item.page_index)}`;
        if (!historyFeedbackPending.has(key)) historyFeedback.set(key, ["like", "dislike", "neutral"].includes(item.feedback) ? item.feedback : "neutral");
        const controls = node("div", "history-feedback"); controls.dataset.feedbackKey = key;
        const buttons = new Map();
        const choices = [["like", "赞"], ["dislike", "踩"], ["neutral", "撤销"]];
        const applyFeedback = (value) => { historyFeedback.set(key, value); document.querySelectorAll(`[data-feedback-key="${CSS.escape(key)}"]`).forEach((group) => group.querySelectorAll("button").forEach((control) => { const choice = control.dataset.feedback; control.setAttribute("aria-pressed", String(choice === value)); control.classList.toggle("is-selected", choice === value); })); };
        const initialFeedback = historyFeedback.get(key);
        choices.forEach(([value, label]) => { const control = button(controls, label, "button button-quiet button-small", async (event) => {
          if (historyFeedbackPending.has(key)) return;
          const previous = historyFeedback.get(key) || "neutral"; if (previous === value) return;
          historyFeedbackPending.add(key); document.querySelectorAll(`[data-feedback-key="${CSS.escape(key)}"] button`).forEach((control) => { control.disabled = true; }); applyFeedback(value);
          try { const result = await api("/admin/api/image-feedback", { method: "PUT", body: JSON.stringify({ illust_id: String(item.illust_id), page_index: Number(item.page_index), value }) }); const saved = ["like", "dislike", "neutral"].includes(result.value) ? result.value : value; applyFeedback(saved); }
          catch (error) { applyFeedback(previous); report(error, "保存图片反馈"); }
          finally { historyFeedbackPending.delete(key); document.querySelectorAll(`[data-feedback-key="${CSS.escape(key)}"] button`).forEach((entry) => { entry.disabled = false; }); }
        }); control.disabled = historyFeedbackPending.has(key); control.dataset.feedback = value; control.setAttribute("aria-pressed", String(value === initialFeedback)); control.classList.toggle("is-selected", value === initialFeedback); buttons.set(value, control); });
        card.append(controls);
        fragment.append(card);
      }
      if (!fragment.children.length) fragment.append(node("p", "empty-cell", "暂无选图记录。升级前已被清理的记录无法恢复。"));
      list.replaceChildren(fragment);
      $("#history-page-status").textContent = "共 " + (result.total || 0) + " 条 · " + (historyPages ? "第 " + historyPage + " / " + historyPages + " 页" : "暂无记录");
    } catch (error) { if (!controller.signal.aborted) { report(error, "读取历史"); if (!list.querySelector(".history-card")) list.replaceChildren(node("p", "empty-cell", "历史暂时无法读取。")); } }
    finally { if (!controller.signal.aborted) { list.setAttribute("aria-busy", "false"); $("#previous-history").disabled = historyPage <= 1; $("#next-history").disabled = historyPage >= historyPages; } }
  }
  $("#reload-history").addEventListener("click", loadHistory);
  $("#previous-history").addEventListener("click", () => { if (historyPage > 1) { historyPage--; loadHistory(); } });
  $("#next-history").addEventListener("click", () => { if (historyPage < historyPages) { historyPage++; loadHistory(); } });
  async function loadStats() {
    const list = $("#stats-grid"); list.setAttribute("aria-busy", "true");
    $("#stats-updated").textContent = "正在读取…";
    try {
      const result = await api("/admin/api/stats");
      const metrics = [
        ["image_pages", "全部图片", "按作品分页计数，包含历史记录"],
        ["artworks", "全部作品", "一个作品可包含多张图片"],
        ["library_artists", "图库画师", "已收录与已屏蔽画师"],
        ["accounts", "Pixiv 账号", "当前未删除的账号"],
        ["indexed_pages", "已索引图片", "不等同于符合当前供图规则"],
        ["unavailable_pages", "不可用图片", "已记录失效或不可用"],
        ["collecting_artists", "启用采集", "持续更新目录的画师"],
        ["blocked_artists", "已屏蔽画师", "始终排除公开供图"],
        ["unknown_rating_artworks", "分级未知作品", "默认不供图，可在偏好中允许"],
        ["unknown_ai_artworks", "AI 状态未知作品", "默认不供图，可在偏好中允许"],
        ["r18_image_pages", "R18 图片", "含 R18G 的已知 R18 图片分页"],
        ["r18_image_ratio", "R18 占比", "含 R18G；分母为全部图片，包含未知分级"]
      ];
      list.replaceChildren();
      for (const [key, label, note] of metrics) {
        const card = node("article", "stat-card");
        const value = key === "r18_image_ratio" ? `${(Math.max(0, Math.min(1, Number(result[key]) || 0) * 100).toFixed(1))}%` : Number(result[key] || 0).toLocaleString();
        card.append(node("p", "muted", label), node("strong", "stat-value", value), node("p", "field-help", note)); list.append(card);
      }
      $("#stats-updated").textContent = "更新于 " + new Date().toLocaleTimeString();
    } catch (error) { $("#stats-updated").textContent = "读取失败，可重新刷新。"; report(error, "读取统计"); }
    finally { list.setAttribute("aria-busy", "false"); }
  }
  $("#reload-stats").addEventListener("click", loadStats);
  async function loadAPIAccounts() {
    try {
      const result = await api("/admin/api/accounts"), select = $("#api-account"), current = select.value;
      select.replaceChildren(new Option("所有来源（包括历史）", ""));
      (result.items || []).forEach((account) => select.add(new Option(account.name || account.id, account.id)));
      if (Array.from(select.options).some((option) => option.value === current)) select.value = current;
      updateAPIURL();
    } catch (error) { report(error, "读取接口账号筛选"); }
  }
  function apiQuery(format) {
    const query = new URLSearchParams({ format });
    for (const [id, key] of [["api-delivery", "delivery"], ["api-account", "account_id"], ["api-artist", "user_id"], ["api-rating", "r18"], ["api-ai", "ai_type"], ["api-orientation", "orientation"], ["api-strategy", "strategy"], ["api-min-width", "min_width"], ["api-min-height", "min_height"]]) {
      const value = $("#" + id).value.trim();
      if (value && !(id === "api-delivery" && value === "proxy") && (value !== "0" || ["api-rating", "api-ai"].includes(id))) query.set(key, value);
    }
    $("#api-include-tags").value.split(/\r?\n/).map((v) => v.trim()).filter(Boolean).forEach((v) => query.append("included_tags", v));
    $("#api-exclude-tags").value.split(/[,，\r\n]+/).map((v) => v.trim()).filter(Boolean).forEach((v) => query.append("excluded_tags", v));
    if (!$("#api-exclude-manga").checked) query.set("exclude_manga", "0");
    return query;
  }
  function updateAPIURL() {
    const imageMode = $("#api-format").value === "image";
    const originalOption = $("#api-delivery").querySelector('option[value="original"]');
    originalOption.disabled = imageMode;
    if (imageMode && $("#api-delivery").value === "original") $("#api-delivery").value = "proxy";
    $("#api-url").value = new URL("/random?" + apiQuery($("#api-format").value), window.location.origin).href;
    $("#api-result").textContent = "";
  }
  $("#api-builder").addEventListener("input", updateAPIURL);
  $("#api-builder").addEventListener("change", updateAPIURL);
  $("#api-builder").addEventListener("submit", async (event) => {
    event.preventDefault();
    if ($("#api-format").value !== "json") { window.open($("#api-url").value, "_blank", "noopener,noreferrer"); return; }
    const trigger = $("#test-api"); trigger.disabled = true;
    const output = $("#api-result"); output.textContent = "正在调用本地目录…";
    try {
      const result = await api("/random?" + apiQuery("json"));
      output.textContent = JSON.stringify(result, null, 2);
    } catch (error) {
      output.textContent = ({ CATALOG_EMPTY: "图库为空，请等待采集完成。", NO_MATCH: "没有符合当前偏好与筛选条件的图片。" })[error.message] || "调用失败：" + error.message;
    } finally { trigger.disabled = false; }
  });
  $("#copy-api-url").addEventListener("click", async () => {
    try { await navigator.clipboard.writeText($("#api-url").value); showMessage("接口地址已复制。"); }
    catch (_) { $("#api-url").focus(); $("#api-url").select(); showMessage("请复制已选中的接口地址。"); }
  });
  $("#go-api-preferences").addEventListener("click", () => { setTab("settings"); $("#preferences-title").scrollIntoView({ block: "start" }); });


  async function loadAccounts() {
    const list = $("#account-list"); list.replaceChildren(node("p", "empty-cell", "正在读取账号…"));
    try {
      const result = await api("/admin/api/accounts");
      const accounts = Array.isArray(result.items) ? result.items : []; accountChoices = accounts;
      updateCopyFollowingChoices(accounts);
      list.replaceChildren();
      if (!accounts.length) { list.append(node("p", "empty-cell", "还没有 Pixiv 账号。添加 Refresh Token 后，服务会按计划同步关注和采集。")); return; }
      accounts.forEach((account) => list.append(renderAccount(account)));
    } catch (error) { list.replaceChildren(node("p", "empty-cell", "账号列表暂时无法读取。")); report(error, "读取账号"); }
  }
  function renderAccount(account) {
    const card = node("article", "account-card");
    card.dataset.accountId = String(account.id);
    const header = node("div", "account-card-heading");
    const identity = node("div"); identity.append(node("h4", "account-name", account.name || "Pixiv 账号"));
    const connection = node("p", "muted", `连接状态：${accountStatus(account.status)}`); connection.dataset.accountConnection = "true"; identity.append(connection);
    const badge = node("span", "account-status-dot"); badge.dataset.accountBadge = "true";
    header.append(identity, badge); card.append(header);
    const sync = node("p", "account-sync-info"); sync.dataset.accountSync = "true"; card.append(sync);
    const errorLine = node("p", "account-error"); errorLine.dataset.accountError = "true"; card.append(errorLine);
    updateAccountCard(card, account);
    const form = node("form", "account-edit-form");
    const nameLabel = node("label", ""); nameLabel.append(document.createTextNode("显示名称"));
    const name = node("input"); name.name = "name"; name.required = true; name.maxLength = 80; name.value = account.name || ""; nameLabel.append(name);
    const tokenLabel = node("label", ""); tokenLabel.append(document.createTextNode("更换 Refresh Token"));
    const token = node("input"); token.type = "password"; token.name = "refresh_token"; token.autocomplete = "new-password"; token.spellcheck = false; token.placeholder = "留空则保留当前凭据"; tokenLabel.append(token);
    form.append(nameLabel, tokenLabel);
    const actions = node("div", "account-actions");
    const save = node("button", "button button-secondary button-small", "保存账号信息"); save.type = "submit"; actions.append(save);
    button(actions, "同步关注", "button button-quiet button-small", async (event) => {
      const actionButton = event.currentTarget; actionButton.disabled = true;
      try { await api(`/admin/api/accounts/${encodeURIComponent(account.id)}/sync`, { method: "POST", body: "{}" }); showMessage("已安排关注同步。同步状态仅说明关注名单同步，不代表作品采集完成。"); await refreshAccountStatuses(); }
      catch (error) { report(error, "安排关注同步"); actionButton.disabled = false; }
    });
    button(actions, "删除账号", "button button-danger button-small", async (event) => {
      if (!window.confirm(`删除账号“${account.name || account.id}”？已保存的画师图库会保留。`)) return;
      const actionButton = event.currentTarget; actionButton.disabled = true;
      try { await api(`/admin/api/accounts/${encodeURIComponent(account.id)}`, { method: "DELETE" }); showMessage("账号已删除；本地画师图库会保留。"); await loadAccounts(); }
      catch (error) { report(error, "删除账号"); actionButton.disabled = false; }
    });
    form.append(actions);
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (save.disabled) return;
      save.disabled = true;
      try {
        await api(`/admin/api/accounts/${encodeURIComponent(account.id)}`, { method: "PUT", body: JSON.stringify({ name: name.value.trim(), refresh_token: token.value }) });
        token.value = ""; showMessage("账号信息已保存；凭据不会回显。"); await loadAccounts();
      } catch (error) { report(error, "保存账号"); }
      finally { save.disabled = false; }
    });
    const options = node("div", "account-options");
    const summary = node("p", "field-help"); summary.dataset.accountOptions = "true"; options.append(summary);
    const defaultButton = button(options, account.is_default ? "默认账号" : "设为默认账号", "button button-secondary button-small", async () => {
      defaultButton.disabled = true;
      try { await api(`/admin/api/accounts/${encodeURIComponent(account.id)}`, { method: "PATCH", body: JSON.stringify({ is_default: true }) }); showMessage("默认账号已更新。"); await loadAccounts(); }
      catch (error) { defaultButton.disabled = false; report(error, "设置默认账号"); }
    }); defaultButton.disabled = !!account.is_default; defaultButton.dataset.makeDefault = "true";
    const followLabel = node("label", "check-option"), follow = node("input"); follow.type = "checkbox"; follow.checked = !!account.follow_on_approve; follow.dataset.followOnApprove = "true";
    followLabel.append(follow, document.createTextNode("批准推荐画师时，同步在此 Pixiv 账号公开关注")); options.append(followLabel);
    follow.addEventListener("change", async () => {
      const enabled = follow.checked; follow.disabled = true;
      try { await api(`/admin/api/accounts/${encodeURIComponent(account.id)}`, { method: "PATCH", body: JSON.stringify({ follow_on_approve: enabled }) }); account.follow_on_approve = enabled; showMessage(enabled ? "已启用推荐画师的远端关注，仅默认账号会执行。" : "已关闭后续自动关注，已有 Pixiv 关注保持不变。"); }
      catch (error) { follow.checked = !enabled; report(error, "保存关注选项"); }
      finally { follow.disabled = false; }
    });
    options.append(node("p", "field-help", "只在你明确加入候选后排队。关闭会取消尚未执行的自动关注；已发出的操作和已有关注不撤销。"));
    card.append(form, options); updateAccountCard(card, account); return card;
  }
  function updateCopyFollowingChoices(accounts) {
    const form = $("#copy-following-form"), source = $("#copy-following-source"), target = $("#copy-following-target");
    const previousSource = source.value, previousTarget = target.value;
    source.replaceChildren(new Option("选择来源账号", ""));
    target.replaceChildren(new Option("选择目标账号", ""));
    accounts.forEach((account) => {
      const optionText = account.name || account.id;
      source.append(new Option(optionText, account.id));
      target.append(new Option(optionText, account.id));
    });
    if (accounts.some((account) => String(account.id) === previousSource)) source.value = previousSource;
    if (accounts.some((account) => String(account.id) === previousTarget)) target.value = previousTarget;
    form.hidden = accounts.length < 2;
    source.disabled = target.disabled = accounts.length < 2;
  }
  $("#copy-following-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget, source = $("#copy-following-source").value, target = $("#copy-following-target").value;
    const submit = form.querySelector("button[type=submit]");
    if (!source || !target) { showMessage("请明确选择来源账号和目标账号。", true); return; }
    if (source === target) { showMessage("来源账号和目标账号必须不同。", true); return; }
    if (submit.disabled) return;
    submit.disabled = true;
    try {
      const result = await api(`/admin/api/accounts/${encodeURIComponent(target)}/copy-following`, { method: "POST", body: JSON.stringify({ source_account_id: source }) });
      showMessage("已安排 " + result.queued + " 位画师，保留来源的公开/非公开状态，已一致的关注会跳过。");
      await refreshAccountStatuses();
    } catch (error) {
      if (error.message === "CONFLICT") showMessage("请先成功同步来源账号的公开和非公开关注，再复制。", true);
      else report(error, "复制关注");
    } finally { submit.disabled = false; }
  });
  function updateAccountCard(card, account) {
    const badgeLabel = account.status === "ready" ? "可用" : account.status === "configured" ? "等待验证" : account.status === "expired" ? "需更新凭据" : "需检查";
    const badge = card.querySelector("[data-account-badge]");
    badge.textContent = badgeLabel;
    badge.className = `account-status-dot ${account.status === "ready" ? "is-ready" : account.status === "configured" ? "is-pending" : ""}`;
    card.querySelector("[data-account-connection]").textContent = `连接状态：${accountStatus(account.status)}`;
    card.querySelector("[data-account-sync]").textContent = `关注同步：${accountStatus(account.sync_state)}${account.last_synced_at ? ` · 最近同步 ${formatTime(account.last_synced_at)}` : " · 尚未同步"}`;
    const options = card.querySelector("[data-account-options]");
    if (options) options.textContent = (account.is_default ? "默认账号 · " : "")  + "已关注 " + (account.followed_artists || 0) + " 位（非公开 " + (account.private_followed_artists || 0) + "） · 待执行关注 " + (account.pending_follows || 0) + " 位";
    const makeDefault = card.querySelector("[data-make-default]"); if (makeDefault) { makeDefault.disabled = !!account.is_default; makeDefault.textContent = account.is_default ? "默认账号" : "设为默认账号"; }
    const error = card.querySelector("[data-account-error]");
    error.textContent = account.error_code ? `最近情况：${accountError(account.error_code)}` : "";
    error.hidden = !account.error_code;
  }
  async function refreshAccountStatuses() {
    if (accountStatusLoading || activeTab !== "settings" || document.visibilityState !== "visible") return;
    accountStatusLoading = true;
    try {
      const result = await api("/admin/api/accounts");
      const accounts = Array.isArray(result.items) ? result.items : [];
      accounts.forEach((account) => {
        const card = $(`.account-card[data-account-id="${CSS.escape(String(account.id))}"]`);
        if (card) updateAccountCard(card, account);
      });
    } catch (_) { /* A failed status poll leaves the last known state visible. */ }
    finally { accountStatusLoading = false; }
  }
  function startAccountPolling() {
    stopAccountPolling();
    if (activeTab === "settings" && document.visibilityState === "visible") accountTimer = window.setInterval(refreshAccountStatuses, 5000);
  }
  function stopAccountPolling() { if (accountTimer !== null) { window.clearInterval(accountTimer); accountTimer = null; } }
  function accountStatus(status) {
    return ({ ready: "已连接", configured: "等待验证", expired: "凭据失效", error: "需要检查", network_error: "网络暂时不可用", auth_required: "凭据需要更新", running: "同步中", succeeded: "已完成", failed: "失败", pending: "等待同步", idle: "尚未同步" })[String(status || "").toLowerCase()] || "未知";
  }
  function accountError(code) {
    return ({
      NETWORK_ERROR: "网络暂时不可用，服务会稍后重试。",
      NETWORK_TIMEOUT: "连接超时，服务会稍后重试。",
      TIMEOUT: "连接超时，服务会稍后重试。",
      AUTH_REQUIRED: "Pixiv 凭据失效，请更新 Refresh Token。",
      UNAUTHORIZED: "Pixiv 凭据失效，请更新 Refresh Token。",
      TOKEN_EXPIRED: "Pixiv 凭据已过期，请更新 Refresh Token。",
      RATE_LIMITED: "Pixiv 请求频繁，服务会稍后重试。",
      RATE_LIMIT: "Pixiv 请求频繁，服务会稍后重试。",
      CREDENTIAL_SAVE_FAILED: "凭据未能安全保存，请稍后重试。",
      PIXIV_UNAVAILABLE: "Pixiv 暂时不可用，服务会稍后重试。"
    })[String(code).toUpperCase()] || "账号暂时无法完成请求，请检查凭据和网络设置。";
  }
  function formatTime(value) { const date = new Date(Number(value)); return Number.isNaN(date.getTime()) ? "时间未知" : date.toLocaleString(); }
  $("#account-create-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const form = event.currentTarget; const data = new FormData(form); const token = String(data.get("refresh_token") || "");
    const submit = form.querySelector("button[type=submit]");
    if (submit.disabled) return;
    submit.disabled = true;
    try {
      await api("/admin/api/accounts", { method: "POST", body: JSON.stringify({ name: String(data.get("name") || "").trim(), refresh_token: token }) });
      form.reset(); showMessage("账号已安全保存；凭据不会回显。"); await loadAccounts();
    } catch (error) { report(error, "添加账号"); }
    finally { submit.disabled = false; }
  });
  $("#reload-accounts").addEventListener("click", loadAccounts);

  let savedProxyAddress = "";
  let proxyHasCredentials = false;
  async function loadNetwork() {
    try {
      const result = await api("/admin/api/settings/network");
      const mode = result.mode || "system";
      const radio = $(`input[name="network-mode"][value="${mode}"]`); if (radio) radio.checked = true;
      savedProxyAddress = result.proxy_address || "";
      proxyHasCredentials = Boolean(result.has_credentials);
      $("#network-proxy-url").value = savedProxyAddress || "http://127.0.0.1:7890";
      const address = result.proxy_address || "未设置";
      $("#network-proxy-current").textContent = `当前模式：${mode === "custom" ? "自定义代理" : mode === "direct" ? "直接连接" : "继承环境"}${mode === "custom" ? ` · 地址 ${address}${result.has_credentials ? "（凭据已保存）" : ""}` : ""}`;
      $("#network-status").textContent = "";
    } catch (error) { report(error, "读取代理设置"); }
  }
  $("#network-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const form = event.currentTarget, submit = form.querySelector("button[type=submit]");
    if (submit.disabled) return;
    submit.disabled = true;
    const mode = $('input[name="network-mode"]:checked')?.value || "system";
    const input = $("#network-proxy-url"), status = $("#network-status"); status.textContent = "正在保存…";
    try {
      const address = input.value.trim();
      // An unchanged redacted address must retain the saved proxy credentials.
      const keepCredentials = proxyHasCredentials && address === savedProxyAddress;
      await api("/admin/api/settings/network", { method: "PUT", body: JSON.stringify({ mode, proxy_url: mode === "custom" && !keepCredentials ? address : "" }) });
      await loadNetwork(); status.textContent = "代理设置已保存，影响后续服务端请求。";
    } catch (error) { status.textContent = "保存失败"; report(error, "保存代理设置"); }
    finally { submit.disabled = false; }
  });
  $("#reload-network").addEventListener("click", loadNetwork);

  function checkedValues(name) { return $$(`input[name="${name}"]:checked`).map((input) => input.value); }
  function setChecked(name, values) { const chosen = new Set(Array.isArray(values) ? values : []); $$(`input[name="${name}"]`).forEach((input) => { input.checked = chosen.has(input.value); }); }
  function parseTagGroups(text) { return text.split(/\r?\n/).map((line) => line.split(",").map((tag) => tag.trim().toLowerCase()).filter(Boolean)).filter((group) => group.length); }
  async function loadPreferences() {
    try {
      const result = await api("/admin/api/settings/preferences"), rules = result.rules || {};
      preferenceVersion = Number(result.version) || 0;
      setChecked("rating", (rules.allowed_ratings || []).map((v) => v === "r18g" ? "r18" : v)); setChecked("ai", rules.allowed_ai);
      $("#min-width").value = String(rules.min_width || 0); $("#min-height").value = String(rules.min_height || 0);
      $("#exclude-manga").checked = !!rules.exclude_manga;
      $("#orientation").value = rules.orientation || "any"; $("#strategy").value = rules.strategy || "balanced";
      $("#include-tags").value = (rules.included_tags || []).map((group) => group.join(", ")).join("\n");
      $("#exclude-tags").value = (rules.excluded_tags || []).join(", ");
      $("#preference-version").textContent = `当前版本 ${preferenceVersion}`;
    } catch (error) { report(error, "读取选图偏好"); }
  }
  $("#preferences-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const submit = event.currentTarget.querySelector("button[type=submit]");
    if (submit.disabled) return;
    submit.disabled = true;
    const rules = {
      allowed_ratings: checkedValues("rating").flatMap((value) => value === "r18" ? ["r18", "r18g"] : [value]), allowed_ai: checkedValues("ai"), exclude_manga: $("#exclude-manga").checked,
      min_width: Number($("#min-width").value), min_height: Number($("#min-height").value),
      orientation: $("#orientation").value, strategy: $("#strategy").value,
      included_tags: parseTagGroups($("#include-tags").value),
      excluded_tags: $("#exclude-tags").value.split(",").map((tag) => tag.trim().toLowerCase()).filter(Boolean)
    };
    try {
      const result = await api("/admin/api/settings/preferences", { method: "PUT", body: JSON.stringify({ rules, version: preferenceVersion }) });
      preferenceVersion = Number(result.version) || preferenceVersion + 1;
      $("#preference-version").textContent = `当前版本 ${preferenceVersion}`; showMessage("选图偏好已保存，只影响选图 API。");
    } catch (error) { report(error, "保存选图偏好"); }
    finally { submit.disabled = false; }
  });

  $("#logout-button").addEventListener("click", async () => {
    try { await api("/admin/logout", { method: "POST", body: "{}" }); window.location.assign("/admin/login"); }
    catch (error) { report(error, "退出登录"); }
  });
  window.PixivCuratorViewer.configure({
    loadArtist: (id) => api(`/admin/api/artists/${encodeURIComponent(id)}`),
    setCollection: (id, enabled) => api(`/admin/api/artists/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ collection_enabled: enabled }) }),
    onChange: (artist) => {
      if (detailArtist && String(detailArtist.id) === String(artist.id)) {
        detailArtist.collection_enabled = artist.collection_enabled;
        const lines = $$("#artist-detail-info > p");
        if (lines[1]) lines[1].textContent = statusText(detailArtist.review_status) + " · " + (artist.collection_enabled ? "采集已启用" : "采集已暂停") + " · " + (detailArtist.remote_followed ? "Pixiv 关注中" : "历史或本地收录");
      }
      if (activeTab === "library") loadLibrary();
    }
  });
  function initialize() {
    setTab("stats");
  }
  initialize();
})();
