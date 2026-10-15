(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const message = $("#global-message");
  let libraryCursor = "";
  let libraryGeneration = 0;
  let preferenceVersion = 0;
  let activeTab = "library";
  let recommendationRunID = "";
  let recommendationTimer = null;
  let recommendationsLoading = false;
  let recommendationPollError = false;
  let knownAccounts = [];
  let accountListLoaded = false;
  let accountTimer = null;
  let accountStatusLoading = false;
  const previewQueue = [];
  let previewWorkers = 0;

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
      NETWORK_ERROR: "网络暂时不可用，服务会稍后重试。"
    };
    showMessage(labels[error.message] || `${context}失败，请稍后重试。`, true);
  }
  function setTab(tabName) {
    activeTab = tabName;
    $$(".tab").forEach((tab) => {
      const selected = tab.dataset.tab === tabName;
      tab.classList.toggle("is-active", selected);
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
    });
    $$(".tab-panel").forEach((panel) => { panel.hidden = panel.dataset.panel !== tabName; });
    if (tabName !== "discover") stopRecommendationPolling();
    if (tabName !== "settings") stopAccountPolling();
    if (tabName === "library") loadLibrary(false);
    if (tabName === "discover") loadLatestRecommendations();
    if (tabName === "settings") { loadAccounts(); loadNetwork(); loadPreferences(); startAccountPolling(); }
  }
  $$(".tab").forEach((tab, index, tabs) => {
    tab.addEventListener("click", () => setTab(tab.dataset.tab));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length;
      tabs[next].focus(); tabs[next].click();
    });
  });

  async function loadLibrary(append) {
    if (!append) { libraryGeneration++; libraryCursor = ""; $("#artist-list").replaceChildren(node("p", "empty-cell", "正在读取图库…")); }
    const generation = libraryGeneration;
    const list = $("#artist-list");
    try {
      const query = new URLSearchParams({ limit: "24" });
      const tag = $("#library-tag").value;
      const account = $("#library-account").value;
      if (tag) query.set("tag", tag);
      if (account) query.set("account_id", account);
      if (libraryCursor) query.set("after", libraryCursor);
      const result = await api(`/admin/api/library/artists?${query}`);
      if (generation !== libraryGeneration) return;
      const artists = Array.isArray(result.items) ? result.items : [];
      if (!append) list.replaceChildren();
      artists.forEach((artist) => list.append(renderArtist(artist)));
      $("#artist-count").textContent = String(list.querySelectorAll(".artist-card").length);
      $("#library-summary").textContent = artists.length ? `本次显示 ${artists.length} 位` : "";
      libraryCursor = result.next_cursor || "";
      $("#more-artists").hidden = !libraryCursor;
      $("#artist-page-status").textContent = libraryCursor ? "向下继续浏览" : (list.children.length ? "已显示全部结果" : "");
      if (!list.children.length) {
        if (!accountListLoaded) await loadLibraryFilters();
        const empty = node("div", "empty-cell");
        const guidance = knownAccounts.length
          ? "图库里暂时还没有符合条件的画师。你可以同步 Pixiv 关注，或手动添加 UID。"
          : "先添加 Pixiv 账号，服务才能同步关注并发现画师；你也可以手动添加 UID。";
        empty.append(node("p", "", guidance));
        if (!knownAccounts.length) button(empty, "前往添加 Pixiv 账号", "button button-primary button-small", () => setTab("settings"));
        list.append(empty);
      }
      loadLibraryFilters();
      hydrateRepresentativeWorks(artists);
    } catch (error) {
      if (generation !== libraryGeneration) return;
      if (!append) list.replaceChildren(node("p", "empty-cell", "图库暂时无法读取。"));
      report(error, "读取图库");
    }
  }
  async function loadLibraryFilters() {
    try {
      const [tagsResult, accountsResult] = await Promise.all([api("/admin/api/library/tags"), api("/admin/api/accounts")]);
      const tagSelect = $("#library-tag"), accountSelect = $("#library-account");
      const selectedTag = tagSelect.value, selectedAccount = accountSelect.value;
      const tags = Array.isArray(tagsResult.items) ? tagsResult.items : [];
      tagSelect.replaceChildren(new Option("所有标签", ""));
      tags.forEach((tag) => tagSelect.add(new Option(tag, tag)));
      if (tags.includes(selectedTag)) tagSelect.value = selectedTag;
      const accounts = Array.isArray(accountsResult.items) ? accountsResult.items : [];
      knownAccounts = accounts;
      accountListLoaded = true;
      accountSelect.replaceChildren(new Option("所有账号", ""));
      accounts.forEach((account) => accountSelect.add(new Option(account.name || account.id, account.id)));
      if (accounts.some((account) => String(account.id) === selectedAccount)) accountSelect.value = selectedAccount;
    } catch (_) { /* Filters are optional while the library API is being deployed. */ }
  }
  function renderArtist(artist) {
    const id = String(artist.id || "");
    const card = node("article", `artist-card${artist.review_status === "blocked" ? " is-blocked" : ""}`);
    card.dataset.artistId = id;
    const cover = node("div", "artist-cover");
    cover.append(node("span", "cover-label", artist.review_status === "blocked" ? "已屏蔽" : "代表作"));
    const sample = artist.representative_artwork || artist.representative_work || artist.artwork || null;
    if (sample) appendArtworkPreview(cover, sample);
    else cover.append(node("span", "cover-empty", "等待作品收录"));
    card.append(cover);
    const body = node("div", "artist-card-body");
    const title = node("div", "artist-title-row");
    title.append(node("h3", "artist-name", artist.name || `Pixiv UID ${id}`));
    title.append(node("span", `status-pill ${artist.review_status || "pending"}`, statusText(artist.review_status)));
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
        showMessage("画师标签已更新。"); await loadLibrary(false);
      } catch (error) { report(error, "保存标签"); }
      finally { save.disabled = false; }
    });
    edit.append(summary, editor, save); body.append(edit);
    const actions = node("div", "artist-actions");
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
    const image = node("img", "cover-image"); image.loading = "lazy"; image.alt = artwork.title || "画师代表作";
    image.src = `/admin/api/previews/${encodeURIComponent(String(artwork.id))}/${encodeURIComponent(String(page.page_index))}?format=redirect`;
    image.addEventListener("error", () => { image.remove(); container.append(node("span", "cover-empty", "预览暂不可用")); }, { once: true });
    container.append(image);
  }
  async function hydrateRepresentativeWorks(artists) {
    const pending = artists.filter((artist) => !artist.representative_artwork && !artist.representative_work && !artist.artwork);
    let next = 0;
    await Promise.all(Array.from({ length: Math.min(3, pending.length) }, async () => {
      while (next < pending.length) {
        const artist = pending[next++];
        try {
          const result = await api(`/admin/api/artworks?artist_id=${encodeURIComponent(String(artist.id))}&limit=1`);
          const artwork = result.items?.[0];
          const card = $(`.artist-card[data-artist-id="${CSS.escape(String(artist.id))}"] .artist-cover`);
          if (artwork && card && !card.querySelector("img")) {
            card.querySelector(".cover-empty")?.remove(); appendArtworkPreview(card, artwork);
            const info = node("span", "cover-caption", artwork.title || "代表作"); card.append(info);
          }
        } catch (_) { /* A missing sample should not hide the artist. */ }
      }
    }));
  }
  function statusText(status) { return ({ approved: "已收录", pending: "正在加入", blocked: "已屏蔽", rejected: "未收录" })[status] || "已收录"; }
  async function libraryArtistAction(id, action, trigger) {
    if (trigger?.disabled) return;
    if (trigger) trigger.disabled = true;
    try {
      await api(`/admin/api/artists/${encodeURIComponent(id)}/${action}`, { method: "POST" });
      const messages = { approve: "画师已加入图库，后台会自动安排采集。", block: "画师已屏蔽，后续不会供图。", unblock: "画师已恢复到图库。", restore: "画师已恢复。" };
      showMessage(messages[action] || "画师状态已更新。"); await loadLibrary(false);
    } catch (error) { report(error, "更新画师状态"); if (trigger) trigger.disabled = false; }
  }
  async function restoreArtistToLibrary(id, trigger) {
    if (trigger?.disabled) return;
    trigger.disabled = true;
    try {
      await api(`/admin/api/artists/${encodeURIComponent(id)}/restore`, { method: "POST" });
      await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" });
      showMessage("画师已恢复并加入图库。"); await loadLibrary(false);
    } catch (error) { report(error, "恢复画师"); trigger.disabled = false; }
  }
  async function toggleCollection(id, enabled, trigger) {
    if (trigger?.disabled) return;
    if (trigger) trigger.disabled = true;
    try { await api(`/admin/api/artists/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ collection_enabled: enabled }) }); showMessage(enabled ? "已恢复后续采集。" : "已暂停后续采集，已入库作品仍保留。"); await loadLibrary(false); }
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
      input.value = ""; showMessage(`UID ${id} 已加入图库，后台会自动安排采集。`); await loadLibrary(false);
    } catch (error) { report(error, "添加画师"); }
    finally { submit.disabled = false; }
  });
  $("#reload-library").addEventListener("click", () => loadLibrary(false));
  $("#more-artists").addEventListener("click", () => loadLibrary(true));
  $("#library-tag").addEventListener("change", () => loadLibrary(false));
  $("#library-account").addEventListener("change", () => loadLibrary(false));

  async function loadLatestRecommendations() {
    if (recommendationsLoading) return;
    recommendationsLoading = true;
    $("#recommendation-list").replaceChildren(node("p", "empty-cell", "正在查找最近一次推荐…"));
    try {
      const result = await api("/admin/api/recommendation-runs/latest");
      const run = result.run || {};
      recommendationRunID = String(run.id || result.run_id || "");
      if (!recommendationRunID) throw new Error("NOT_FOUND");
      if (recommendationPollError) { recommendationPollError = false; showMessage(""); }
      renderRecommendations(run, Array.isArray(result.items) ? result.items : []);
      if (["pending", "running", "retry_wait"].includes(run.state)) startRecommendationPolling();
      else stopRecommendationPolling();
    } catch (error) {
      if (error.message === "NOT_FOUND") {
        stopRecommendationPolling();
        $("#recommendation-status").replaceChildren(node("span", "run-state", "还没有推荐记录"));
        $("#recommendation-list").replaceChildren(node("p", "empty-cell", "点击“开始发现”，生成第一批候选画师。"));
      } else {
        recommendationPollError = true;
        report(error, "读取最近推荐");
        if (activeTab === "discover" && document.visibilityState === "visible") startRecommendationPolling();
      }
    } finally { recommendationsLoading = false; }
  }
  $("#start-recommendations").addEventListener("click", async (event) => {
    const trigger = event.currentTarget;
    if (trigger.disabled) return;
    trigger.disabled = true; trigger.textContent = "正在提交…";
    try {
      const result = await api("/admin/api/recommendations", { method: "POST" });
      recommendationRunID = String(result.run_id || "");
      showMessage("已开始发现画师。结果会自动更新；候选不会自动加入图库。");
      await loadRecommendationByID(recommendationRunID);
      startRecommendationPolling();
    } catch (error) { report(error, "开始发现"); }
    finally { trigger.disabled = false; trigger.textContent = "开始发现"; }
  });
  async function loadRecommendationByID(runID) {
    if (!runID) return loadLatestRecommendations();
    if (recommendationsLoading) return;
    recommendationsLoading = true;
    try {
      const result = await api(`/admin/api/recommendations?run_id=${encodeURIComponent(runID)}`);
      renderRecommendations(result.run || { id: runID }, Array.isArray(result.items) ? result.items : []);
    } catch (error) { report(error, "读取推荐结果"); }
    finally { recommendationsLoading = false; }
  }
  function startRecommendationPolling() {
    stopRecommendationPolling();
    if (activeTab === "discover" && document.visibilityState === "visible") recommendationTimer = window.setInterval(() => loadLatestRecommendations(), 2500);
  }
  function stopRecommendationPolling() { if (recommendationTimer !== null) { window.clearInterval(recommendationTimer); recommendationTimer = null; } }
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState !== "visible") { stopRecommendationPolling(); stopAccountPolling(); }
    else {
      if (activeTab === "discover") loadLatestRecommendations();
      if (activeTab === "settings") { refreshAccountStatuses(); startAccountPolling(); }
    }
  });
  function renderRecommendations(run, items) {
    const status = $("#recommendation-status"); status.replaceChildren();
    status.append(node("span", "run-state", "为你发现的画师"));
    status.append(node("span", `run-pill state-${run.state || "unknown"}`, runLabel(run.state)));
    status.append(node("span", "run-meta", `已发起 ${Number(run.request_count) || 0} 次上游请求`));
    const list = $("#recommendation-list"); list.replaceChildren();
    if (!items.length) {
      const empty = run.state === "succeeded" ? "这一批暂时没有可展示的候选。" : "推荐正在整理候选，页面会自动更新。";
      list.append(node("p", "empty-cell", empty)); return;
    }
    items.slice(0, 60).forEach((item) => list.append(renderRecommendation(item)));
  }
  function runLabel(state) { return ({ pending: "排队中", running: "正在发现", retry_wait: "稍后重试", succeeded: "已完成", failed: "未完成", paused_auth: "需要检查账号" })[state] || "状态更新中"; }
  function renderRecommendation(item) {
    const artist = item.artist || {}, id = String(artist.id || item.artist_id || "");
    const card = node("article", "recommendation-card");
    const cover = node("div", "recommendation-cover");
    const title = node("h3", "candidate-name", artist.name || `Pixiv UID ${id}`);
    const sampleIDs = Array.isArray(item.sample_illust_ids) ? item.sample_illust_ids.slice(0, 4) : [];
    const samples = node("div", "candidate-samples");
    const content = node("div", "recommendation-content");
    if (sampleIDs.length) {
      content.append(samples);
      sampleIDs.slice(0, 1).forEach((workID) => queueCandidateSample(id, workID, cover));
      sampleIDs.slice(1).forEach((workID) => queueCandidateSample(id, workID, samples));
    } else cover.append(node("span", "cover-empty", "暂无代表作预览"));
    const heading = node("div", "candidate-heading");
    const identity = node("div"); identity.append(title, node("p", "artist-id", `UID ${id}${artist.account ? ` · ${artist.account}` : ""}`)); heading.append(identity);
    const score = Number(item.score);
    heading.append(node("span", "score-badge", Number.isFinite(score) ? `匹配度 ${score.toFixed(2)}` : "推荐候选")); content.append(heading);
    const reasons = Array.isArray(item.reasons) ? item.reasons : [];
    const chips = node("div", "reason-list");
    if (reasons.length) reasons.slice(0, 5).forEach((reason) => chips.append(node("span", "reason-chip", reason)));
    else chips.append(node("span", "reason-chip", "根据图库兴趣发现"));
    content.append(chips);
    const actions = node("div", "candidate-actions");
    button(actions, "加入图库", "button button-primary", async (event) => {
      const actionButton = event.currentTarget; actionButton.disabled = true;
      try { await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" }); showMessage("已加入图库，后台会自动安排采集。"); card.remove(); }
      catch (error) { actionButton.disabled = false; report(error, "加入图库"); }
    });
    button(actions, "不感兴趣", "button button-quiet", async (event) => {
      const actionButton = event.currentTarget; actionButton.disabled = true;
      try { await api(`/admin/api/artists/${encodeURIComponent(id)}/reject`, { method: "POST" }); showMessage("已记录为不感兴趣。"); card.remove(); }
      catch (error) { actionButton.disabled = false; report(error, "记录选择"); }
    });
    content.append(actions); card.append(cover, content); return card;
  }
  function queueCandidateSample(artistID, artworkID, container) {
    previewQueue.push({ artistID, artworkID, container });
    pumpPreviewQueue();
  }
  function pumpPreviewQueue() {
    while (previewWorkers < 4 && previewQueue.length) {
      const item = previewQueue.shift(); previewWorkers++;
      loadCandidateSample(item.artistID, item.artworkID, item.container).finally(() => { previewWorkers--; pumpPreviewQueue(); });
    }
  }
  async function loadCandidateSample(artistID, artworkID, container) {
    try {
      const result = await api(`/admin/api/artworks?artist_id=${encodeURIComponent(artistID)}&limit=4`);
      const artwork = (result.items || []).find((item) => String(item.id) === String(artworkID)) || result.items?.[0];
      if (!artwork) { if (!container.children.length) container.append(node("span", "cover-empty", "暂无代表作预览")); return; }
      const sample = node("div", "sample-tile"); appendArtworkPreview(sample, artwork); sample.append(node("span", "sample-caption", artwork.title || "作品预览")); container.append(sample);
    } catch (_) { if (!container.children.length) container.append(node("span", "cover-empty", "预览暂不可用")); }
  }
  $("#close-artworks").addEventListener("click", () => $("#artwork-dialog").close());

  async function loadAccounts() {
    const list = $("#account-list"); list.replaceChildren(node("p", "empty-cell", "正在读取账号…"));
    try {
      const result = await api("/admin/api/accounts");
      const accounts = Array.isArray(result.items) ? result.items : [];
      knownAccounts = accounts;
      accountListLoaded = true;
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
    card.append(form); return card;
  }
  function updateAccountCard(card, account) {
    const badgeLabel = account.status === "ready" ? "可用" : account.status === "configured" ? "等待验证" : account.status === "expired" ? "需更新凭据" : "需检查";
    const badge = card.querySelector("[data-account-badge]");
    badge.textContent = badgeLabel;
    badge.className = `account-status-dot ${account.status === "ready" ? "is-ready" : account.status === "configured" ? "is-pending" : ""}`;
    card.querySelector("[data-account-connection]").textContent = `连接状态：${accountStatus(account.status)}`;
    card.querySelector("[data-account-sync]").textContent = `关注同步：${accountStatus(account.sync_state)}${account.last_synced_at ? ` · 最近同步 ${formatTime(account.last_synced_at)}` : " · 尚未同步"}`;
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
      knownAccounts = accounts;
      accountListLoaded = true;
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

  async function loadNetwork() {
    try {
      const result = await api("/admin/api/settings/network");
      const mode = result.mode || "system";
      const radio = $(`input[name="network-mode"][value="${mode}"]`); if (radio) radio.checked = true;
      $("#network-proxy-url").value = "";
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
      const result = await api("/admin/api/settings/network", { method: "PUT", body: JSON.stringify({ mode, proxy_url: mode === "custom" ? input.value.trim() : "" }) });
      input.value = ""; await loadNetwork(); status.textContent = "代理设置已保存，影响后续服务端请求。";
      if (result.has_credentials) input.value = "";
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
      setChecked("rating", rules.allowed_ratings); setChecked("ai", rules.allowed_ai);
      $("#min-width").value = String(rules.min_width || 0); $("#min-height").value = String(rules.min_height || 0);
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
      allowed_ratings: checkedValues("rating"), allowed_ai: checkedValues("ai"),
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
  function initialize() {
    loadLibrary(false);
    loadLibraryFilters();
  }
  initialize();
})();
