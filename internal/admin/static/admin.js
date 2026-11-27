(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const message = $("#global-message");
  let messageTimer;
  let libraryPage = 1, libraryPages = 0, libraryLoading = false;
  let detailController = null, detailArtist = null, detailCursors = [""], detailPage = 0;
  let libraryGeneration = 0;
  const libraryArtists = new Map();
  let preferenceVersion = 0;
  let activeTab = "stats";
  let recommendationRunID = "";
  let recommendationTimer = null;
  let recommendationController = null;
  let recommendationPreviewController = null;
  let recommendationPollError = false;
  let recommendationRenderedKey = "", recommendationPendingID = "";
  let tagController = null, tagSearchTimer = null;
  let accountTimer = null;
  let accountStatusLoading = false;
  let accountChoices = [];
  let historyPage = 1, historyPages = 0, historyController = null;
  let libraryReviewController = null, libraryReviewRound = 0, libraryRemovalPending = false;
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
    window.clearTimeout(messageTimer);
    message.textContent = text;
    message.classList.toggle("is-error", error);
    message.hidden = !text;
    if (text) messageTimer = window.setTimeout(() => showMessage(""), 6000);
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
    if (tabName === "library") {
      if ($("#library-review").open) loadLibraryReview();
      if ($("#library-list-panel").open) { loadLibrary(); loadLibraryFilters(); }
    } else { libraryReviewController?.abort(); }
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

  async function loadLibrary(reset = false, preserve = false) {
    if (reset) libraryPage = 1;
    if (!$("#library-list-panel").open) return;
    const generation = ++libraryGeneration;
    libraryLoading = true; updateLibraryPager();
    const list = $("#artist-list");
    list.setAttribute("aria-busy", "true");
    if (!preserve) list.replaceChildren(node("p", "empty-cell", "正在读取图库…"));
    try {
      const query = new URLSearchParams({ limit: $("#library-page-size").value, page: String(libraryPage) });
      const tag = $("#library-tag").value, account = $("#library-account").value, collection = $("#library-collection").value;
      const uid = $("#library-uid").value.trim(), order = $("#library-sort").value;
      if (uid) query.set("uid", uid);
      if (order) query.set("sort", order);
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
      libraryArtists.clear();
      artists.forEach((artist) => {
        libraryArtists.set(String(artist.id), artist);
        list.append(renderArtist(artist));
      });
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
      if (!preserve) list.replaceChildren(node("p", "empty-cell", "图库暂时无法读取，请点击刷新重试。"));
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
    const select = $("#library-tag"), selected = select.value, hint = $("#library-tag-hint");
    const query = new URLSearchParams({ limit: "30", q: $("#library-tag-search").value.trim() });
    try {
      const result = await api("/admin/api/library/tags?" + query, { signal: controller.signal });
      if (controller.signal.aborted) return;
      const tags = (Array.isArray(result.items) ? result.items : []).slice(0, 30);
      const options = document.createDocumentFragment(); options.append(new Option("所有标签", ""));
      if (selected && !tags.includes(selected)) options.append(new Option(selected, selected));
      tags.forEach((tag) => options.append(new Option(tag, tag)));
      select.replaceChildren(options); select.value = selected;
      hint.textContent = tags.length ? "" : "没有匹配标签，可换一个关键词。";
      hint.hidden = tags.length > 0;
    } catch (error) {
      if (!controller.signal.aborted) {
        hint.textContent = "标签暂不可用，仍可浏览图库。";
        hint.hidden = false;
      }
    }
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
  async function loadLibraryReview(next = false) {
    if (libraryRemovalPending || activeTab !== "library" || !$("#library-review").open) return;
    libraryReviewController?.abort(); const controller = new AbortController(); libraryReviewController = controller;
    const list = $("#library-review-list"), trigger = $("#reload-library-review");
    const hasCards = !!list.querySelector(".library-review-card");
    list.setAttribute("aria-busy", "true");
    if (!hasCards) list.replaceChildren(node("p", "empty-cell", "正在读取…"));
    list.inert = true;
    trigger.disabled = true;
    trigger.textContent = next ? "换组中…" : "读取中…";
    let timedOut = false;
    const timeout = window.setTimeout(() => { timedOut = true; controller.abort(); }, next ? 40000 : 10000);
    try {
      const result = next
        ? await api("/admin/api/library/review/next", { method: "POST", body: JSON.stringify({ round: libraryReviewRound }), signal: controller.signal })
        : await api("/admin/api/library/review", { signal: controller.signal });
      if (timedOut) throw new Error("TIMEOUT");
      if (controller.signal.aborted || !$("#library-review").open) return;
      libraryReviewRound = result.round;
      const kept = new Set(result.kept_ids || []);
      const fragment = document.createDocumentFragment();
      (Array.isArray(result.items) ? result.items : []).slice(0, 6).forEach((item) => {
        const artist = item.artist || {}, id = String(artist.id || "");
        const card = node("article", "library-review-card"); card.dataset.artistId = id;
        const heading = node("div", "library-review-heading");
        const identity = node("div"), name = node("h3", "artist-name"), profile = node("a", "candidate-title-button", artist.name || "Pixiv UID " + id); profile.href = `https://www.pixiv.net/users/${encodeURIComponent(id)}`; profile.target = "_blank"; profile.rel = "noopener noreferrer"; name.append(profile); identity.append(name, node("p", "artist-id", "UID " + id)); heading.append(identity);
        const keep = button(heading, kept.has(id) ? "已标记保留" : "保留", "button button-quiet button-small", async () => {
          keep.disabled = true;
          try {
            await api(`/admin/api/library/artists/${encodeURIComponent(id)}/keep`, {
              method: "POST", body: JSON.stringify({ round: result.round })
            });
            keep.textContent = "已标记保留";
          } catch (error) { keep.disabled = false; report(error, "保留画师"); }
        });
        keep.disabled = kept.has(id);
        card.append(heading);
        const reasons = { dislikes: "净踩较多", large_catalog: "图库较大", low_availability: "供图可用率偏低", recent_ai: "近期出现 AI", recent_manga: "近期漫画较多", no_preview: "长期无图", new_ai: "冷却期间出现 AI", new_dislikes: "冷却期间新增踩", routine: "常规巡检" };
        card.append(node("p", "field-help", (reasons[item.audit_reason] || "常规巡检") + " · " + artistSummaryText(item.summary || {})));
        const samples = node("div", "library-review-samples");
        (Array.isArray(item.samples) ? item.samples : (item.sample_illust_ids || []).map((illust_id) => ({ illust_id, page_index: 0 }))).slice(0, 6).forEach((sample) => {
          const illustID = String(sample.illust_id), pageIndex = Number(sample.page_index) || 0;
          const tile = node("button", "sample-tile"); tile.type = "button"; tile.setAttribute("aria-label", "查看作品 " + illustID + " 第 " + (pageIndex + 1) + " 页大图");
          tile.addEventListener("click", () => window.PixivCuratorViewer.open({ id: illustID, artistId: id, pageIndex, title: "代表作 " + illustID, artistName: artist.name || "" }));
          const image = node("img", "cover-image"); image.loading = "lazy"; image.decoding = "async"; image.alt = "代表作 " + illustID; image.src = `/admin/api/previews/${encodeURIComponent(illustID)}/${pageIndex}`; tile.append(image);
          if (sample.reason) tile.append(node("span", "sample-badge", sample.reason === "dislike" ? "已踩" : "AI"));
          samples.append(tile);
        });
        if (samples.children.length) card.append(samples);
        const actions = node("div", "library-review-actions");
        const rejectLabel = node("label", "field-help");
        const reject = node("input"); reject.type = "checkbox"; rejectLabel.append(reject, document.createTextNode(" 不感兴趣")); actions.append(rejectLabel);
        const trim = button(actions, item.trimmed ? "本轮已减半" : "减半收录", "button button-quiet button-small", async () => {
          if (trim.disabled || libraryRemovalPending) return;
          if (!window.confirm("保留最新的 90% 首图，再随机用次页补到原图片数的一半。清理后，被删图片的固定链接将失效。确定减半收录？")) return;
          libraryRemovalPending = true;
          trigger.disabled = true;
          list.inert = true;
          trim.textContent = "处理中…";
          let refresh = false;
          try {
            const trimmed = await api(`/admin/api/library/artists/${encodeURIComponent(id)}/trim`, {
              method: "POST", body: JSON.stringify({ round: result.round })
            });
            refresh = true;
            showMessage(trimmed.before === trimmed.after
              ? "当前图片数已达到保留首图的下限。"
              : `图片收录已从 ${trimmed.before} 页减至 ${trimmed.after} 页。`);
          } catch (error) {
            refresh = error.message === "CONFLICT";
            report(error, "减半收录");
          } finally {
            libraryRemovalPending = false;
            trigger.disabled = false;
            list.inert = false;
            trim.textContent = "减半收录";
            if (refresh) await loadLibraryReview();
          }
        });
        trim.disabled = item.trimmed || Number(item.summary?.pages || 0) < 2;
        button(actions, "移出图库", "button button-danger button-small", async (event) => {
          const remove = event.currentTarget;
          if (remove.disabled || libraryRemovalPending) return;
          const controller = new AbortController();
          const timeout = window.setTimeout(() => controller.abort(), 15000);
          libraryRemovalPending = true;
          remove.disabled = true;
          remove.textContent = "移出中…";
          trigger.disabled = true;
          list.inert = true;
          list.setAttribute("aria-busy", "true");
          let reconcile = false;
          try {
            await api(`/admin/api/library/artists/${encodeURIComponent(id)}/remove`, {
              method: "POST",
              body: JSON.stringify({ not_interested: reject.checked, round: result.round }),
              signal: controller.signal
            });
            card.remove();
            showMessage("已暂停并清理该画师作品，Pixiv 关注关系保留。");
            if (!list.querySelector(".library-review-card")) list.replaceChildren(node("p", "empty-cell", "本组已整理完。"));
            if (activeTab === "library" && $("#library-list-panel").open) loadLibrary(false, true);
          } catch (error) {
            reconcile = controller.signal.aborted || error.message === "REMOVE_TIMEOUT" || error.message === "CONFLICT";
            if (reconcile) showMessage("正在核对移出结果…");
            else report(error, "移出图库");
          } finally {
            window.clearTimeout(timeout);
            libraryRemovalPending = false;
            remove.disabled = false;
            remove.textContent = "移出图库";
            trigger.disabled = false;
            list.inert = false;
            list.setAttribute("aria-busy", "false");
            if (reconcile) {
              libraryReviewRound = 0;
              await loadLibraryReview();
            }
          }
        });
        card.append(actions); fragment.append(card);
      });
      if (!fragment.children.length) fragment.append(node("p", "empty-cell", "这轮没有可整理的画师，可以换一组。"));
      list.replaceChildren(fragment);
    } catch (error) {
      if (libraryReviewController !== controller) return;
      if (next && error.message === "CONFLICT" && !controller.signal.aborted) {
        libraryReviewRound = 0;
        return loadLibraryReview();
      }
      if (controller.signal.aborted && !timedOut) return;
      // A timed-out POST may have committed. Read the current group on retry.
      if (next) libraryReviewRound = 0;
      if (timedOut || error.message === "REVIEW_NEXT_TIMEOUT") {
        showMessage(next ? "生成新组超时，下次会先核对当前批次。" : "读取当前整理批次超时，请稍后重试。", true);
      }
      else report(error, "读取整理建议");
      if (!hasCards) list.replaceChildren(node("p", "empty-cell", "暂时无法读取，稍后可重试。"));
    } finally {
      window.clearTimeout(timeout);
      if (libraryReviewController === controller) {
        list.setAttribute("aria-busy", "false");
        list.inert = false;
        trigger.disabled = false;
        trigger.textContent = "换一组";
      }
    }
  }
  function rememberLibraryPanel(panel) {
    const key = "curator.admin." + panel.id + ".open";
    try {
      const saved = localStorage.getItem(key);
      if (saved === "true" || saved === "false") panel.open = saved === "true";
    } catch (_) { /* Storage may be unavailable in private browsing. */ }
    panel.addEventListener("toggle", () => {
      try { localStorage.setItem(key, String(panel.open)); } catch (_) {}
    });
  }
  rememberLibraryPanel($("#library-review"));
  rememberLibraryPanel($("#library-list-panel"));
  $("#library-review").addEventListener("toggle", () => { if ($("#library-review").open) loadLibraryReview(); else libraryReviewController?.abort(); });
  $("#reload-library-review").addEventListener("click", () => loadLibraryReview(libraryReviewRound > 0));
  $("#library-list-panel").addEventListener("toggle", () => {
    if (activeTab === "library" && $("#library-list-panel").open) {
      loadLibrary();
      loadLibraryFilters();
    }
  });
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
  function updateLibraryCollection(id, enabled) {
    const artist = libraryArtists.get(String(id));
    const card = $("#artist-list").querySelector('[data-artist-id="' + CSS.escape(String(id)) + '"]');
    if (!artist || !card) return;
    artist.collection_enabled = enabled;
    card.replaceWith(renderArtist(artist));
    const filtered = $("#library-collection").value !== "" || $("#library-tag").value !== "";
    if (filtered || $("#library-sort").value.startsWith("artworks_")) loadLibrary(false, true);
  }
  async function toggleCollection(id, enabled, trigger) {
    if (trigger?.disabled) return;
    if (trigger) trigger.disabled = true;
    try { await api(`/admin/api/artists/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ collection_enabled: enabled }) }); showMessage(enabled ? "已恢复后续采集。" : "已停止采集和供图，并清理作品与链接。"); updateLibraryCollection(id, enabled); }
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
  let libraryUIDTimer;
  $("#library-uid").addEventListener("input", () => { clearTimeout(libraryUIDTimer); libraryUIDTimer = setTimeout(() => loadLibrary(true), 300); });
  $("#library-sort").addEventListener("change", () => loadLibrary(true));
  $("#library-tag").addEventListener("change", () => loadLibrary(true));
  $("#library-account").addEventListener("change", () => loadLibrary(true));
  $("#library-collection").addEventListener("change", () => loadLibrary(true));

  function matchesRecommendationCategory(filter, actual) {
    if (filter === "all" || filter === actual) return true;
    return filter.endsWith("_or_unknown") && (actual === "unknown" || actual === filter.slice(0, -"_or_unknown".length));
  }
  function recommendationQuery(runID = "") {
    const query = new URLSearchParams({ content_rating: $("#recommendation-rating").value, ai_type: $("#recommendation-ai").value });
    if (runID) query.set("run_id", runID);
    return query;
  }
  function updateRecommendationStatus(run, itemCounts) {
    const status = $("#recommendation-status");
    const progress = Math.max(0, Math.min(100, Number(run.progress_percent) || 0));
    const sources = run.budget?.actual_source_counts;
    const generated = sources ? ["algorithm", "independent_ranking", "independent_uniform_id"]
      .reduce((total, source) => total + (Number(sources[source]) || 0), 0) : null;
    const key = [run.id, run.state, progress, !!run.budget?.directed, generated, itemCounts?.pending, itemCounts?.visible].join(":");
    const running = ["pending", "running", "retry_wait"].includes(run.state);
    $("#start-recommendations").disabled = running;
    $("#start-recommendations").textContent = running ? "正在发现…" : "换一批";
    if (status.dataset.stateKey === key) return;
    status.dataset.stateKey = key;
    const parts = document.createDocumentFragment();
    parts.append(node("span", "run-state", run.budget?.directed ? "定向随机发现" : "为你发现的画师"));
    parts.append(node("span", `run-pill state-${run.state || "unknown"}`, runLabel(run.state)));
    if (itemCounts) {
      const counts = [generated === null ? "" : "本轮生成 " + generated + " 位", "待处理 " + itemCounts.pending + " 位"];
      if (itemCounts.visible !== itemCounts.pending) counts.push("当前显示 " + itemCounts.visible + " 位");
      parts.append(node("span", "run-meta", counts.filter(Boolean).join(" · ")));
    } else parts.append(node("span", "run-meta", progress + "%"));
    status.replaceChildren(parts);
  }
  const recommendationPreviewQueue = [];
  const recommendationPreviewSignals = new WeakMap();
  let recommendationPreviewObserver = null, recommendationPreviewActive = 0;
  const recommendationPreviewConcurrency = 3;
  function finishRecommendationPreview(image, state) {
    const task = image.__previewTask;
    if (!task || task.finished) return;
    task.finished = true;
    clearTimeout(task.timer);
    image.removeEventListener("load", task.loaded);
    image.removeEventListener("error", task.failed);
    task.signal.removeEventListener("abort", task.canceled);
    delete image.__previewTask;
    delete image.dataset.previewLoading;
    recommendationPreviewActive = Math.max(0, recommendationPreviewActive - 1);
    const tile = image.closest(".sample-tile"), placeholder = tile?.querySelector(".preview-placeholder");
    if (state === "loaded") {
      image.dataset.previewLoaded = "true";
      image.hidden = false;
      placeholder?.remove();
      if (tile) { delete tile.dataset.previewFailed; tile.setAttribute("aria-label", "查看作品 " + image.alt.replace(/^代表作\s*/, "") + " 大图"); }
      const card = image.closest(".recommendation-card"), approval = card?.querySelector("[data-approve]");
      if (card) card.dataset.previewReady = "true";
      if (approval && !card.dataset.decisionPending) { approval.disabled = false; approval.removeAttribute("title"); }
    } else if (state === "failed") {
      image.removeAttribute("src");
      image.hidden = true;
      if (placeholder) { placeholder.className = "preview-placeholder preview-retry-hint"; placeholder.textContent = "加载失败 · 点击重试"; }
      if (tile) { tile.dataset.previewFailed = "true"; tile.setAttribute("aria-label", "预览加载失败，点击重试：" + image.alt); }
    } else {
      image.removeAttribute("src");
      image.dataset.previewSrc = image.dataset.previewURL;
      image.hidden = false;
    }
    pumpRecommendationPreviewQueue();
  }
  function pumpRecommendationPreviewQueue() {
    recommendationPreviewQueue.sort((a, b) => Number(a.image.dataset.sampleIndex || 0) - Number(b.image.dataset.sampleIndex || 0));
    while (recommendationPreviewActive < recommendationPreviewConcurrency && recommendationPreviewQueue.length) {
      const { image, signal } = recommendationPreviewQueue.shift();
      delete image.dataset.previewQueued;
      if (signal.aborted || !image.isConnected || image.dataset.previewLoaded === "true" || !image.dataset.previewSrc) continue;
      const tile = image.closest(".sample-tile"), placeholder = tile?.querySelector(".preview-placeholder");
      const src = image.dataset.previewSrc;
      delete image.dataset.previewSrc;
      if (tile) delete tile.dataset.previewFailed;
      placeholder?.classList.remove("preview-retry-hint");
      if (placeholder) placeholder.textContent = "正在加载预览";
      image.hidden = false;
      image.loading = "eager";
      image.fetchPriority = Number(image.dataset.sampleIndex || 0) === 0 ? "high" : "auto";
      image.dataset.previewLoading = "true";
      recommendationPreviewActive++;
      const task = { signal, timer: null, finished: false };
      task.loaded = () => finishRecommendationPreview(image, "loaded");
      task.failed = () => finishRecommendationPreview(image, "failed");
      task.canceled = () => finishRecommendationPreview(image, "canceled");
      image.__previewTask = task;
      image.addEventListener("load", task.loaded, { once: true });
      image.addEventListener("error", task.failed, { once: true });
      signal.addEventListener("abort", task.canceled, { once: true });
      task.timer = setTimeout(task.failed, 7000);
      image.src = src;
    }
  }
  function queueRecommendationPreview(image, signal = recommendationPreviewSignals.get(image)) {
    if (!signal || signal.aborted || !image.isConnected || image.dataset.previewLoaded === "true" || image.dataset.previewQueued || image.dataset.previewLoading || !image.dataset.previewSrc) return;
    image.dataset.previewQueued = "true";
    recommendationPreviewQueue.push({ image, signal });
    pumpRecommendationPreviewQueue();
  }
  function queueVisibleRecommendationPreviews() {
    $$("#recommendation-list .candidate-samples img[data-preview-src]").forEach((image) => {
      const slider = image.closest(".candidate-samples");
      const bounds = slider?.getBoundingClientRect(), rect = image.getBoundingClientRect();
      if (bounds && rect.right > bounds.left - 80 && rect.left < bounds.right + 80 && rect.bottom > -220 && rect.top < innerHeight + 220) queueRecommendationPreview(image);
    });
  }
  function retryRecommendationPreview(image) {
    const tile = image.closest(".sample-tile");
    if (!tile || tile.dataset.previewFailed !== "true") return;
    delete tile.dataset.previewFailed;
    const placeholder = tile.querySelector(".preview-placeholder");
    if (placeholder) { placeholder.className = "preview-placeholder"; placeholder.textContent = "正在加载预览"; }
    image.hidden = false;
    image.dataset.previewSrc = image.dataset.previewURL;
    queueRecommendationPreview(image);
  }
  function observeRecommendationPreviews(root, signal) {
    const images = Array.from(root.querySelectorAll("img[data-preview-src]"));
    if (!signal || signal.aborted) return;
    if (!recommendationPreviewObserver && "IntersectionObserver" in window) {
      recommendationPreviewObserver = new IntersectionObserver((entries) => {
        entries.forEach((entry) => {
          if (!entry.isIntersecting) return;
          recommendationPreviewObserver.unobserve(entry.target);
          queueRecommendationPreview(entry.target);
        });
      }, { rootMargin: "220px 180px", threshold: 0.01 });
    }
    signal.addEventListener("abort", () => {
      const batchImages = new Set(images);
      for (let i = recommendationPreviewQueue.length - 1; i >= 0; i--) {
        if (batchImages.has(recommendationPreviewQueue[i].image)) recommendationPreviewQueue.splice(i, 1);
      }
      images.forEach((image) => {
        recommendationPreviewObserver?.unobserve(image);
        delete image.dataset.previewQueued;
        if (image.__previewTask?.signal === signal) image.__previewTask.canceled();
        else if (image.dataset.previewLoaded !== "true") {
          image.removeAttribute("src");
          image.dataset.previewSrc = image.dataset.previewURL;
        }
      });
    }, { once: true });
    images.forEach((image) => {
      recommendationPreviewSignals.set(image, signal);
      const slider = image.closest(".candidate-samples");
      if (slider && !slider.dataset.previewScrollBound) {
        slider.dataset.previewScrollBound = "true";
        slider.addEventListener("scroll", () => {
          const bounds = slider.getBoundingClientRect();
          slider.querySelectorAll("img[data-preview-src]").forEach((candidate) => {
            const rect = candidate.getBoundingClientRect();
            if (rect.right > bounds.left - 180 && rect.left < bounds.right + 180 && rect.bottom > -220 && rect.top < innerHeight + 220) queueRecommendationPreview(candidate);
          });
        }, { passive: true });
      }
      if (recommendationPreviewObserver) recommendationPreviewObserver.observe(image);
      else if (slider) slider.dispatchEvent(new Event("scroll"));
    });
    if (!recommendationPreviewObserver && !document.documentElement.dataset.previewScrollBound) {
      document.documentElement.dataset.previewScrollBound = "true";
      document.addEventListener("scroll", queueVisibleRecommendationPreviews, { capture: true, passive: true });
    }
  }
  // An exposure requires a continuously visible card and a visible loaded sample.
  let exposureRunID = "", exposureEligible = new Set(), exposureSince = new Map(), exposureObserver = null;
  function resetDiscoveryExposure(runID = "") {
    exposureObserver?.disconnect(); exposureRunID = runID;
    exposureEligible = new Set(); exposureSince = new Map();
  }
  function visibleIntersection(rect, bounds) {
    return Math.max(0, Math.min(rect.right, bounds.right) - Math.max(rect.left, bounds.left)) * Math.max(0, Math.min(rect.bottom, bounds.bottom) - Math.max(rect.top, bounds.top));
  }
  function updateDiscoveryExposure() {
    if (!exposureRunID || activeTab !== "discover" || document.visibilityState !== "visible") {
      exposureSince.clear();
      return;
    }
    const viewport = {left: 0, top: 0, right: innerWidth, bottom: innerHeight};
    for (const card of $$("#recommendation-list .recommendation-card[data-artist-id]")) {
      const id = card.dataset.artistId, rect = card.getBoundingClientRect();
      const targetArea = Math.min(rect.width, innerWidth) * Math.min(rect.height, innerHeight);
      const cardVisible = targetArea > 0 && visibleIntersection(rect, viewport) >= targetArea * 0.98;
      const sampleVisible = Array.from(card.querySelectorAll('img[data-preview-loaded="true"]')).some((image) => {
        const imageRect = image.getBoundingClientRect(), slider = image.closest(".candidate-samples")?.getBoundingClientRect();
        if (!slider || image.hidden) return false;
        const clipped = {left: Math.max(slider.left, viewport.left), top: Math.max(slider.top, viewport.top), right: Math.min(slider.right, viewport.right), bottom: Math.min(slider.bottom, viewport.bottom)};
        return visibleIntersection(imageRect, clipped) > 0;
      });
      if (!cardVisible || !sampleVisible || card.dataset.decisionPending) { exposureSince.delete(id); continue; }
      if (!exposureSince.has(id)) exposureSince.set(id, performance.now());
      if (performance.now() - exposureSince.get(id) >= 1500) exposureEligible.add(id);
    }
  }
  function observeDiscoveryExposure(runID, root) {
    if (exposureRunID !== runID) resetDiscoveryExposure(runID);
    exposureObserver?.disconnect();
    if ("IntersectionObserver" in window) {
      exposureObserver = new IntersectionObserver(updateDiscoveryExposure, {threshold: [0, 0.25, 0.5, 0.75, 1]});
      root.querySelectorAll(".recommendation-card[data-artist-id]").forEach((card) => exposureObserver.observe(card));
    }
    updateDiscoveryExposure();
  }
  setInterval(updateDiscoveryExposure, 250);
  document.addEventListener("visibilitychange", () => { if (document.visibilityState !== "visible") exposureSince.clear(); });
  function prepareRecommendationCards(items) {
    const fragment = document.createDocumentFragment();
    const oldCards = new Map($$("#recommendation-list .recommendation-card[data-artist-id]").map((card) => [card.dataset.artistId, card]));
    const cards = items.filter((item) => Array.isArray(item.sample_illust_ids) && item.sample_illust_ids.length).slice(0, 30).map((item) => {
      const id = String(item.artist_id || item.artist?.id || "");
      if (item.components?.carryover && oldCards.has(id)) { const stub = node("article"); stub.dataset.reuseArtist = id; return stub; }
      return renderRecommendation(item);
    });
    cards.forEach((card) => fragment.append(card));
    fragment.querySelectorAll("[data-reuse-artist]").forEach((stub) => { const old = oldCards.get(stub.dataset.reuseArtist); if (old?.isConnected) stub.replaceWith(old); else stub.remove(); });
    return fragment;
  }
  async function readRecommendations(runID = "", invalidate = false) {
    stopRecommendationPolling(); recommendationController?.abort();
    const controller = new AbortController(); recommendationController = controller;
    const list = $("#recommendation-list");
    if (invalidate) { recommendationPreviewController?.abort(); recommendationPreviewController = null; recommendationRenderedKey = ""; list.replaceChildren(node("p", "empty-cell", "正在读取推荐…")); }
    try {
      const path = runID ? "/admin/api/recommendations?" : "/admin/api/recommendation-runs/latest?";
      const result = await api(path + new URLSearchParams(runID ? { run_id: runID } : {}), { signal: controller.signal });
      if (controller.signal.aborted || activeTab !== "discover") return;
      const run = result.run || {};
      recommendationRunID = String(run.id || runID || "");
      if (!recommendationRunID) throw new Error("NOT_FOUND");
      if (recommendationPollError) { recommendationPollError = false; showMessage(""); }
      if (run.state !== "succeeded") updateRecommendationStatus(run);
      if (["pending", "running", "retry_wait"].includes(run.state)) {
        if (!list.querySelector(".recommendation-card") && (recommendationPendingID !== recommendationRunID || !list.children.length || invalidate)) {
          list.replaceChildren(node("p", "empty-cell", "正在收集候选与代表作，完成后一次展示；候选不会自动加入图库。"));
          recommendationPendingID = recommendationRunID; recommendationRenderedKey = "";
        }
        if (!list.querySelector(".recommendation-card")) {
          const carried = (result.items || []).filter((item) => item.components?.carryover && matchesRecommendationCategory($("#recommendation-rating").value, item.content_rating) && matchesRecommendationCategory($("#recommendation-ai").value, item.ai_status));
          if (carried.length) {
            recommendationPreviewController?.abort();
            const previewController = new AbortController(); recommendationPreviewController = previewController;
            const carriedFragment = prepareRecommendationCards(carried);
            if (!controller.signal.aborted) { list.replaceChildren(carriedFragment); observeRecommendationPreviews(list, previewController.signal); }
          }
        }
        startRecommendationPolling(); return;
      }
      recommendationPendingID = "";
      if (run.state !== "succeeded") {
        list.replaceChildren(node("p", "empty-cell", run.state === "paused_auth" ? "请先在设置中检查账号，恢复任务后再查看。" : "本轮发现未完成，可以稍后重新开始。")); return;
      }
      const rating = $("#recommendation-rating").value, ai = $("#recommendation-ai").value;
      const pending = Array.isArray(result.items) ? result.items : [];
      const items = pending.filter((item) => matchesRecommendationCategory(rating, item.content_rating) && matchesRecommendationCategory(ai, item.ai_status));
      const visible = items.filter((item) => Array.isArray(item.sample_illust_ids) && item.sample_illust_ids.length).length;
      updateRecommendationStatus(run, { pending: pending.length, visible });
      const key = JSON.stringify([run.id, recommendationQuery().toString(), items]);
      if (key === recommendationRenderedKey) { startRecommendationPolling(30000); return; }
      recommendationPreviewController?.abort();
      const previewController = new AbortController(); recommendationPreviewController = previewController;
      const fragment = prepareRecommendationCards(items);
      if (!fragment || controller.signal.aborted || activeTab !== "discover") return;
      if (!fragment.children.length) fragment.append(node("p", "empty-cell", "当前筛选下没有带代表作的候选，可调整筛选或生成下一批。"));
      list.replaceChildren(fragment); observeRecommendationPreviews(list, previewController.signal); observeDiscoveryExposure(run.budget?.directed ? "" : recommendationRunID, list); recommendationRenderedKey = key; startRecommendationPolling(30000);
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
  $("#recommendation-rating").addEventListener("change", () => { resetDiscoveryExposure(); readRecommendations("", true); });
  $("#recommendation-ai").addEventListener("change", () => { resetDiscoveryExposure(); readRecommendations("", true); });
  async function submitDiscovery(options) {
    const trigger = $("#start-recommendations");
    if (trigger.disabled) return;
    stopRecommendationPolling();
    recommendationController?.abort(); recommendationPreviewController?.abort(); recommendationPreviewController = null;
    trigger.disabled = true;
    try {
      const path = options ? "/admin/api/recommendations/directed" : "/admin/api/recommendations";
      const request = { method: "POST" };
      if (options) request.body = JSON.stringify(options);
      else request.body = JSON.stringify({parent_run: exposureRunID, exposed_ids: Array.from(exposureEligible).filter((id) => document.querySelector(`#recommendation-list .recommendation-card[data-artist-id="${CSS.escape(id)}"]`))});
      const result = await api(path, request);
      recommendationRunID = String(result.run_id || "");
      if (options) {
        $("#recommendation-rating").value = "all";
        $("#recommendation-ai").value = "all";
      }
      showMessage(options ? "已提交定向随机发现。" : "已提交发现任务。");
      await readRecommendations(recommendationRunID, true);
    } catch (error) {
      trigger.disabled = false;
      report(error, options ? "定向随机发现" : "开始发现");
      startRecommendationPolling();
    }
  }
  $("#toggle-directed-discovery").addEventListener("click", (event) => {
    const form = $("#directed-discovery");
    form.hidden = !form.hidden;
    event.currentTarget.setAttribute("aria-expanded", String(!form.hidden));
  });
  $("#directed-discovery").addEventListener("submit", (event) => {
    event.preventDefault();
    if (event.currentTarget.hidden) { submitDiscovery(null); return; }
    const data = new FormData(event.currentTarget);
    const tags = String(data.get("tags")).split(/[,，、]/).map((tag) => tag.trim()).filter(Boolean);
    const options = { tags, rating: data.get("rating"), ai: data.get("ai"), orientation: data.get("orientation") };
    if (!tags.length && options.rating === "all" && options.ai === "all" && options.orientation === "any") {
      showMessage("请填写标签或选择至少一个条件。", true);
      return;
    }
    submitDiscovery(options);
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
  function runLabel(state) { return ({ pending: "排队中", running: "正在发现", retry_wait: "稍后重试", succeeded: "已完成", failed: "未完成", paused_auth: "需要检查账号" })[state] || "状态更新中"; }
  function renderRecommendation(item) {
    const artist = item.artist || {}, id = String(artist.id || item.artist_id || "");
    const card = node("article", "recommendation-card");
    card.dataset.artistId = id;
    const sampleIDs = Array.isArray(item.sample_illust_ids) ? item.sample_illust_ids.slice(0, 3) : [];
    const title = node("h3", "candidate-name");
    const profile = node("a", "candidate-title-button", artist.name || "Pixiv UID " + id);
    profile.href = `https://www.pixiv.net/users/${encodeURIComponent(id)}`;
    profile.target = "_blank"; profile.rel = "noopener noreferrer";
    profile.setAttribute("aria-label", "打开 " + (artist.name || "Pixiv UID " + id) + " 的 Pixiv 主页");
    title.append(profile);
    const samples = node("div", "candidate-samples");
    const content = node("div", "recommendation-content");
    const heading = node("div", "candidate-heading");
    const identity = node("div"); identity.append(title, node("p", "artist-id", `UID ${id}${artist.account ? ` · ${artist.account}` : ""}`)); heading.append(identity);
    const score = Number(item.components?.display_score ?? item.score);
    const directedRandom = Array.isArray(item.sources) && item.sources.includes("directed_random");
    if (!item.components?.uniform_id && !item.components?.exploration) {
      const scoreBadge = node("span", "score-badge", directedRandom ? "定向随机" : Number.isFinite(score) ? `推荐指数 ${score.toFixed(2)}` : "推荐候选");
      scoreBadge.title = directedRandom ? "来自定向随机候选，不表示模型评分" : "仅用于候选排序参考，不代表喜欢概率";
      heading.append(scoreBadge);
    }
    content.append(heading);
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
        const image = node("img", "cover-image"); image.alt = "代表作 " + workID; image.decoding = "async"; image.loading = "lazy";
        image.dataset.sampleIndex = String(sampleIndex); image.dataset.previewURL = `/admin/api/previews/${encodeURIComponent(workID)}/0`; image.dataset.previewSrc = image.dataset.previewURL;
        const placeholder = node("span", "preview-placeholder", "等待加载预览");
        tile.addEventListener("click", (event) => {
          if (tile.dataset.previewFailed === "true") { event.preventDefault(); event.stopPropagation(); retryRecommendationPreview(image); return; }
          window.PixivCuratorViewer.open({ id: workID, artistId: artist.id, pageIndex: 0, title: "代表作 " + workID, artistName: artist.name || "" });
        });
        tile.append(image, placeholder);
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
      decisionPending = true; card.dataset.decisionPending = "true"; approveButton.disabled = true; rejectButton.disabled = true;
      try {
        await api(`/admin/api/artists/${encodeURIComponent(id)}/approve`, { method: "POST" });
        showMessage("已加入图库，后台会自动安排采集。");
        if (exploring && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
          emitExplorationApprovalParticles(card);
          setTimeout(() => { card.remove(); startRecommendationPolling(0); }, 475);
        } else { card.remove(); startRecommendationPolling(0); }
      } catch (error) {
        decisionPending = false; delete card.dataset.decisionPending; actionButton.disabled = card.dataset.previewReady !== "true"; rejectButton.disabled = false;
        report(error, "加入图库");
      }
    });
    approveButton.dataset.approve = "true"; approveButton.disabled = true;
    const rejectButton = button(actions, "不感兴趣", "button button-quiet", async (event) => {
      const actionButton = event.currentTarget;
      if (decisionPending) return;
      decisionPending = true; card.dataset.decisionPending = "true"; approveButton.disabled = true; rejectButton.disabled = true;
      try { await api(`/admin/api/artists/${encodeURIComponent(id)}/reject`, { method: "POST" }); showMessage("已记录为不感兴趣。"); card.remove(); startRecommendationPolling(0); }
      catch (error) { decisionPending = false; delete card.dataset.decisionPending; actionButton.disabled = false; approveButton.disabled = card.dataset.previewReady !== "true"; report(error, "记录选择"); }
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
  function artistSummaryText(summary) {
    return `作品 ${summary.artworks || 0} · 图片 ${summary.pages || 0} · 默认可出图 ${summary.eligible_pages || 0} · R18 ${summary.r18 || 0}/${summary.known_rating || 0}（已知） · AI ${summary.ai || 0}/${summary.known_ai || 0}（已知） · 赞 ${summary.likes || 0} / 踩 ${summary.dislikes || 0}${summary.latest_published_at ? " · 最近 " + new Date(summary.latest_published_at).toLocaleDateString() : ""}`;
  }
  async function loadArtistSummary(artist) {
    const line = $("#artist-detail-summary");
    try {
      const summary = await api("/admin/api/library/artists/" + encodeURIComponent(artist.id) + "/summary");
      if (detailArtist === artist && line.isConnected) line.textContent = artistSummaryText(summary);
    } catch (_) {
      if (detailArtist === artist && line.isConnected) line.textContent = "作品统计暂时不可用。";
    }
  }
  async function openArtistDetails(artist) {
    closeArtistDetails(); detailArtist = artist; detailCursors = [""]; detailPage = 0;
    $("#artwork-heading").textContent = artist.name || "Pixiv UID " + artist.id;
    const info = $("#artist-detail-info"); info.replaceChildren();
    info.append(node("p", "muted", "UID " + artist.id + (artist.account ? " · " + artist.account : "")));
    info.append(node("p", "muted", statusText(artist.review_status) + " · " + (artist.collection_enabled ? "采集已启用" : "采集已暂停") + " · " + (artist.remote_followed ? "Pixiv 关注中" : "历史或本地收录")));
    const summary = node("p", "field-help", "正在读取作品统计…");
    summary.id = "artist-detail-summary";
    info.append(summary);
    loadArtistSummary(artist);
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
        const pageIndices = (artwork.pages || []).filter((page) => page.media_state === "indexed").map((page) => page.page_index);
        const canOpen = artwork.visible && pageIndices.length > 0;
        const open = () => window.PixivCuratorViewer.open({
          id: artwork.id, artistId: artwork.artist_id || detailArtist?.id, pageIndex: pageIndices[0], pageCount: artwork.page_count,
          pageIndices, title: artwork.title, artistName: detailArtist?.name || "", rating: artwork.rating, aiStatus: artwork.ai_status, tags: artwork.tags
        });
        if (canOpen) {
          appendArtworkPreview(cover, artwork); cover.classList.add("image-open-target"); cover.tabIndex = 0; cover.setAttribute("role", "button"); cover.setAttribute("aria-label", "查看作品大图");
          cover.addEventListener("click", open); cover.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(); } });
        } else cover.append(node("span", "cover-empty", artwork.visible ? "暂无可用预览" : "作品当前不可见"));
        const info = node("div", "artwork-info");
        const title = node("div", "artwork-title-row");
        if (canOpen) {
          const titleButton = node("button", "artwork-title artwork-title-button", artwork.title || "作品 " + artwork.id);
          titleButton.type = "button"; titleButton.setAttribute("aria-label", "查看作品大图：" + (artwork.title || artwork.id));
          titleButton.addEventListener("click", open);
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

  const historyJump = $("#history-page-jump");
  const historyPageNumber = $("#history-page-number");
  async function loadHistory(page = historyPage) {
    historyController?.abort(); const controller = new AbortController(); historyController = controller;
    const list = $("#history-list"); list.setAttribute("aria-busy", "true");
    const jumpButton = historyJump.querySelector("button");
    $("#previous-history").disabled = true; $("#next-history").disabled = true;
    historyPageNumber.disabled = jumpButton.disabled = true;
    try {
      const result = await api("/admin/api/history?" + new URLSearchParams({ page: String(page), limit: "24" }), { signal: controller.signal });
      if (controller.signal.aborted || activeTab !== "history") return;
      const totalPages = Number(result.total_pages) || 0;
      if (totalPages && page > totalPages) return loadHistory(totalPages);
      historyPages = totalPages;
      historyPage = totalPages ? page : 1;
      const fragment = document.createDocumentFragment();
      let artistRequest = 0;
      for (const item of result.items || []) {
        const card = node("article", "history-card"); card.dataset.historyId = String(item.id);
        const open = () => window.PixivCuratorViewer.open({ id: item.illust_id, artistId: item.artist_id, pageIndex: item.page_index, pageCount: item.page_count, pageIndices: item.page_indices, title: item.title, artistName: item.artist_name, rating: item.rating, aiStatus: item.ai_status, tags: item.tags });
        const cover = node("button", "history-preview"); cover.type = "button"; cover.disabled = !item.available;
        if (item.available) {
          const image = node("img"); image.alt = item.title || "历史图片"; image.loading = "lazy"; image.decoding = "async";
          image.src = `/admin/api/previews/${encodeURIComponent(item.illust_id)}/${Number(item.page_index)}`;
          image.addEventListener("error", () => image.replaceWith(node("span", "cover-empty", "图片暂不可用")), { once: true }); cover.append(image);
          cover.addEventListener("click", open);
        } else cover.append(node("span", "cover-empty", "当前不可预览"));
        const title = node("h3", "artwork-title");
        if (item.available) {
          const titleButton = node("button", "artwork-title-button", item.title || "作品 " + item.illust_id);
          titleButton.type = "button"; titleButton.setAttribute("aria-label", "查看作品大图：" + (item.title || item.illust_id));
          titleButton.addEventListener("click", open);
          title.append(titleButton);
        } else title.textContent = item.title || "作品 " + item.illust_id;
        const meta = node("p", "artwork-meta");
        if (item.artist_id && item.artist_id !== "0") {
          button(meta, item.artist_name || "UID " + item.artist_id, "artist-detail-link", async () => {
            const request = ++artistRequest;
            try {
              const artist = await api("/admin/api/artists/" + encodeURIComponent(item.artist_id), { signal: controller.signal });
              if (!controller.signal.aborted && activeTab === "history" && request === artistRequest) {
                await openArtistDetails(artist);
              }
            } catch (error) {
              if (!controller.signal.aborted && request === artistRequest) report(error, "读取画师详情");
            }
          });
          meta.append(" · ");
        }
        meta.append("第 " + (Number(item.page_index) + 1) + " 页");
        card.append(cover, title, meta, node("p", "artwork-meta", formatTime(item.selected_at)));
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
    finally {
      if (!controller.signal.aborted) {
        list.setAttribute("aria-busy", "false");
        $("#previous-history").disabled = historyPage <= 1;
        $("#next-history").disabled = historyPage >= historyPages;
        historyPageNumber.value = String(historyPage);
        historyPageNumber.max = String(Math.max(1, historyPages));
        historyPageNumber.disabled = jumpButton.disabled = !historyPages;
      }
    }
  }
  $("#reload-history").addEventListener("click", () => loadHistory());
  $("#previous-history").addEventListener("click", () => { if (historyPage > 1) loadHistory(historyPage - 1); });
  $("#next-history").addEventListener("click", () => { if (historyPage < historyPages) loadHistory(historyPage + 1); });
  historyJump.addEventListener("submit", (event) => {
    event.preventDefault();
    const page = historyPageNumber.valueAsNumber;
    if (!historyPageNumber.disabled && Number.isInteger(page) && page >= 1 && page <= historyPages) loadHistory(page);
  });
  async function loadStats() {
    const list = $("#stats-grid"); list.setAttribute("aria-busy", "true");
    $("#stats-updated").textContent = "正在读取…";
    try {
      const result = await api("/admin/api/stats");
      const total = Number(result.image_pages) || 0;
      const eligible = Number(result.eligible_image_pages) || 0;
      const ratio = (count, denominator) => denominator > 0 ? (count / denominator * 100).toFixed(1) + "%" : "—";
      const metrics = [
        ["image_pages", "全部图片", "按作品分页计数，包含历史记录"],
        ["artworks", "全部作品", "一个作品可包含多张图片"],
        ["library_artists", "图库画师", "已收录与已屏蔽画师"],
        ["accounts", "Pixiv 账号", "当前未删除的账号"],
        ["indexed_pages", "已索引图片", "不等同于符合当前供图规则"],
        ["unavailable_pages", "不可用图片", "已记录失效或不可用"],
        ["collecting_artists", "启用采集", "持续更新目录的画师"],
        ["blocked_artists", "已屏蔽画师", "始终排除公开供图"],
        ["eligible_image_pages", "默认可出图图片", "占全部图片 " + ratio(eligible, total)],
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
      const unknown = Number(result.unknown_orientation_pages) || 0;
      const known = total - unknown;
      const directions = [["横图", "landscape_pages"], ["竖图", "portrait_pages"], ["方图", "square_pages"]];
      for (const [label, key] of directions) {
        const count = Number(result[key]) || 0;
        const card = node("article", "stat-card");
        card.title = "大字为占已知尺寸图片的比例";
        card.append(node("p", "muted", label), node("strong", "stat-value", ratio(count, known)),
          node("p", "field-help", `占全部图片 ${ratio(count, total)}`));
        list.append(card);
      }
      const unknownCard = node("article", "stat-card");
      unknownCard.append(node("p", "muted", "尺寸未知"), node("strong", "stat-value", ratio(unknown, total)),
        node("p", "field-help", `${unknown.toLocaleString()} 张缺少宽高`));
      list.append(unknownCard);
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
    if ($("#api-orientation-fallback").checked) query.set("orientation_fallback", "first_page");
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
      $("#orientation").value = rules.orientation || "any"; $("#strategy").value = rules.strategy || "stratified";
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
        loadArtistSummary(detailArtist);
        loadArtistDetails();
        const lines = $$("#artist-detail-info > p");
        if (lines[1]) lines[1].textContent = statusText(detailArtist.review_status) + " · " + (artist.collection_enabled ? "采集已启用" : "采集已暂停") + " · " + (detailArtist.remote_followed ? "Pixiv 关注中" : "历史或本地收录");
      }
      if (activeTab === "library") updateLibraryCollection(artist.id, artist.collection_enabled);
    }
  });
  function initialize() {
    setTab("stats");
  }
  initialize();
})();
