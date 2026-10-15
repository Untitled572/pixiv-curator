(() => {
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const globalMessage = $("#global-message");
  let artistCursor = "";
  let preferenceVersion = 0;
  const selectedArtists = new Set();

  function showMessage(text, isError = false) {
    globalMessage.textContent = text;
    globalMessage.classList.toggle("is-error", isError);
    globalMessage.hidden = !text;
  }
  function csrfToken() {
    const prefix = "curator_csrf=";
    const part = document.cookie.split(";").map((value) => value.trim()).find((value) => value.startsWith(prefix));
    return part ? decodeURIComponent(part.slice(prefix.length)) : "";
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
      const code = payload && payload.error && payload.error.code;
      const error = new Error(code || `HTTP_${response.status}`);
      error.status = response.status;
      throw error;
    }
    return payload;
  }
  function node(tag, className, text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = String(text);
    return element;
  }
  function addButton(parent, label, className, handler) {
    const button = node("button", className, label);
    button.type = "button";
    button.addEventListener("click", handler);
    parent.append(button);
    return button;
  }
  function reportError(error, context) {
    const labels = {
      UNAUTHORIZED: "登录已过期，请重新登录。",
      csrf_failed: "会话校验失败，请刷新页面后重试。",
      CONFLICT: "当前状态已变化，请刷新后重试。",
      INVALID_INPUT: "提交内容不符合要求。",
      NOT_FOUND: "记录不存在或当前不可用。",
      NOT_READY: "本地服务暂时不可用。"
    };
    showMessage(labels[error.message] || `${context}失败，请稍后重试。`, true);
  }
  function statusLabel(status) {
    const labels = { pending: "待审核", approved: "已批准", rejected: "已拒绝", blocked: "已屏蔽", succeeded: "已完成", running: "运行中", retry_wait: "等待重试", paused_auth: "等待凭据", failed: "失败", canceled: "已取消" };
    return labels[status] || status || "未知";
  }

  $$(".tab").forEach((tab) => tab.addEventListener("click", () => {
    $$(".tab").forEach((item) => item.classList.toggle("is-active", item === tab));
    $$(".tab-panel").forEach((panel) => { panel.hidden = panel.dataset.panel !== tab.dataset.tab; });
    if (tab.dataset.tab === "artists") loadArtists(false);
    if (tab.dataset.tab === "recommendations") $("#recommendation-run-id").focus();
    if (tab.dataset.tab === "jobs") loadJobs();
    if (tab.dataset.tab === "preferences") loadPreferences();
    if (tab.dataset.tab === "account") { loadAccount(); loadPixivLogin(); }
  }));

  async function loadArtists(append) {
    if (!append) { artistCursor = ""; selectedArtists.clear(); updateBulkButton(); $("#artist-rows").replaceChildren(node("tr", "", "")); }
    const rowBody = $("#artist-rows");
    if (!append) {
      const row = node("tr"); const cell = node("td", "empty-cell", "正在读取画师…"); cell.colSpan = 7; row.append(cell); rowBody.replaceChildren(row);
    }
    try {
      const query = new URLSearchParams({ limit: "50" });
      if (artistCursor) query.set("cursor", artistCursor);
      const result = await api(`/admin/api/artists?${query.toString()}`);
      if (!append) rowBody.replaceChildren();
      const items = Array.isArray(result.items) ? result.items : [];
      items.forEach((artist) => rowBody.append(renderArtist(artist)));
      if (!append && items.length === 0) {
        const row = node("tr"); const cell = node("td", "empty-cell", "还没有画师。可手动导入 UID，或在账号页同步关注。"); cell.colSpan = 7; row.append(cell); rowBody.append(row);
      }
      $("#artist-count").textContent = String(rowBody.querySelectorAll("tr[data-artist]").length);
      $("#artist-summary").textContent = items.length ? `本页 ${items.length} 位画师` : "";
      artistCursor = result.next_cursor || "";
      $("#more-artists").hidden = !artistCursor;
    } catch (error) { reportError(error, "读取画师"); }
  }
  function renderArtist(artist) {
    const row = node("tr"); row.dataset.artist = "true";
    const selectCell = node("td");
    if (artist.review_status === "pending") {
      const checkbox = node("input", "row-check"); checkbox.type = "checkbox"; checkbox.setAttribute("aria-label", `选择 UID ${artist.id} 供批量批准`);
      checkbox.checked = selectedArtists.has(String(artist.id));
      checkbox.addEventListener("change", () => { if (checkbox.checked) selectedArtists.add(String(artist.id)); else selectedArtists.delete(String(artist.id)); updateBulkButton(); });
      selectCell.append(checkbox);
    }
    const identity = node("td"); identity.append(node("span", "artist-name", artist.name || `Pixiv UID ${artist.id}`), node("span", "artist-id", `UID ${artist.id}`));
    const following = node("td", artist.remote_followed ? "bool-on" : "bool-off", artist.remote_followed ? "关注中" : "未关注");
    const status = node("td"); status.append(node("span", `pill ${artist.review_status || "neutral"}`, statusLabel(artist.review_status)));
    const collection = node("td", artist.collection_enabled ? "bool-on" : "bool-off", artist.collection_enabled ? "允许采集" : "暂停采集");
    const canServe = artist.review_status === "approved";
    const serving = node("td", canServe ? "supply-on" : "supply-off", canServe ? "已批准" : "未开放");
    const actions = node("td"); const actionGroup = node("div", "row-actions");
    addButton(actionGroup, "作品预览", "button button-quiet button-small", () => openArtworks(artist.id, artist.name || `UID ${artist.id}`));
    const id = encodeURIComponent(String(artist.id));
    if (artist.review_status === "pending") {
      addButton(actionGroup, "批准并排队", "button button-primary button-small", () => artistAction(id, "approve"));
      addButton(actionGroup, "拒绝", "button button-quiet button-small", () => artistAction(id, "reject"));
      addButton(actionGroup, "屏蔽", "button button-quiet button-small", () => artistAction(id, "block"));
    } else if (artist.review_status === "blocked" || artist.review_status === "rejected") {
      addButton(actionGroup, "恢复为待审核", "button button-secondary button-small", () => artistAction(id, "restore"));
    } else if (artist.review_status === "approved") {
      addButton(actionGroup, artist.collection_enabled ? "暂停采集" : "恢复采集", "button button-secondary button-small", () => toggleCollection(id, !artist.collection_enabled));
      addButton(actionGroup, "屏蔽供图", "button button-quiet button-small", () => artistAction(id, "block"));
    }
    actions.append(actionGroup); row.append(selectCell, identity, following, status, collection, serving, actions); return row;
  }
  function updateBulkButton() {
    const button = $("#approve-selected");
    if (!button) return;
    button.textContent = `批准选中（${selectedArtists.size}）`;
    button.disabled = selectedArtists.size === 0;
  }
  $("#approve-selected").addEventListener("click", async () => {
    const ids = Array.from(selectedArtists);
    if (!ids.length) { showMessage("请先勾选要批准的待审核画师。", true); return; }
    if (!window.confirm(`你已选择 ${ids.length} 位画师。批准后会为每位画师创建采集任务；这不表示作品已采集完成。继续吗？`)) return;
    try {
      const result = await api("/admin/api/artists/approve-batch", { method: "POST", body: JSON.stringify({ ids }) });
      const approved = (result.results || []).filter((item) => item.status === "approved").length;
      const failed = ids.length - approved;
      showMessage(`批量审核完成：${approved} 位已批准并排队${failed ? `，${failed} 位未通过（可能状态已变化）` : ""}。任务完成情况请查看后台任务。`, failed > 0);
      selectedArtists.clear(); await loadArtists(false); await loadJobs();
    } catch (error) { reportError(error, "批量审核"); }
  });
  async function artistAction(id, action) {
    try {
      const result = await api(`/admin/api/artists/${id}/${action}`, { method: "POST" });
      if (action === "approve") showMessage(`画师已批准，采集任务 ${result.job_id || "已创建"} 已排队。队列状态不代表采集已完成。`);
      else showMessage(`画师状态已更新为“${statusLabel(action === "restore" ? "pending" : action === "block" ? "blocked" : "rejected")}”。`);
      await loadArtists(false); await loadJobs();
    } catch (error) { reportError(error, "更新画师"); }
  }
  async function toggleCollection(id, enabled) {
    try {
      await api(`/admin/api/artists/${id}`, { method: "PATCH", body: JSON.stringify({ collection_enabled: enabled }) });
      showMessage(enabled ? "已恢复采集，现有作品仍保留。" : "已暂停后续采集，已入库作品仍可供图。");
      await loadArtists(false);
    } catch (error) { reportError(error, "更新采集状态"); }
  }
  $("#import-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const input = $("#artist-id"); const id = input.value.trim();
    try {
      await api("/admin/api/artists", { method: "POST", body: JSON.stringify({ id }) });
      input.value = ""; showMessage(`UID ${id} 已加入待审核名单；尚未批准或开始采集。`); await loadArtists(false);
    } catch (error) { reportError(error, "导入画师"); }
  });
  $("#reload-artists").addEventListener("click", () => loadArtists(false));
  $("#more-artists").addEventListener("click", () => loadArtists(true));

  async function loadJobs() {
    const list = $("#job-list"); list.replaceChildren(node("p", "empty-cell", "正在读取任务…"));
    try {
      const result = await api("/admin/api/jobs?limit=100");
      list.replaceChildren();
      const jobs = Array.isArray(result.items) ? result.items : [];
      if (!jobs.length) { list.append(node("p", "empty-cell", "暂无后台任务。")); return; }
      jobs.forEach((job) => list.append(renderJob(job)));
    } catch (error) { list.replaceChildren(node("p", "empty-cell", "无法读取任务，请检查会话后重试。")); reportError(error, "读取任务"); }
  }
  function renderJob(job) {
    const card = node("article", "job-card");
    const info = node("div");
    const kinds = { sync_artist: "采集画师作品", sync_following: "同步 Pixiv 关注", hydrate_artwork: "补全作品信息", repair_media: "修复图片链接", recommend_artists: "推荐画师" };
    info.append(node("div", "job-title", kinds[job.kind] || job.kind), node("div", "job-meta", `任务 ${job.id} · 尝试 ${job.attempt || 0}`));
    const status = node("div", "job-status"); status.append(node("span", `pill ${job.state || "neutral"}`, statusLabel(job.state)));
    if (job.error_code) status.append(node("span", "job-code", job.error_code));
    const actions = node("div", "job-actions");
    if (["pending", "running", "retry_wait", "paused_auth"].includes(job.state)) addButton(actions, "取消任务", "button button-quiet button-small", () => jobAction(job.id, "cancel"));
    if (job.state === "paused_auth") addButton(actions, "凭据修复后恢复", "button button-secondary button-small", () => jobAction(job.id, "resume"));
    if (job.state === "failed" || job.state === "canceled") addButton(actions, "重新排队", "button button-secondary button-small", () => jobAction(job.id, "retry"));
    card.append(info, status, actions); return card;
  }
  async function jobAction(id, action) {
    try {
      const result = await api(`/admin/api/jobs/${encodeURIComponent(String(id))}/${action}`, { method: "POST" });
      showMessage(action === "resume" ? "任务已恢复为待处理。" : action === "retry" ? `新任务 ${result.job_id || "已创建"} 已排队。` : "已请求取消任务；运行中的任务会在安全边界停止。"); await loadJobs();
    } catch (error) { reportError(error, "更新任务"); }
  }
  $("#reload-jobs").addEventListener("click", loadJobs);

  $("#start-recommendations").addEventListener("click", async (event) => {
    const button = event.currentTarget; button.disabled = true;
    try {
      const result = await api("/admin/api/recommendations", { method: "POST" });
      $("#recommendation-run-id").value = result.run_id || "";
      showMessage(`推荐任务 ${result.job_id || "已创建"} 已排队，批次 ${result.run_id || "已生成"}。任务仍在后台运行，不会自动批准候选。`);
      renderRunSummary({ id: result.run_id, state: "pending", request_count: 0 });
      $("#recommendation-list").replaceChildren(node("p", "empty-cell", "任务已排队。可稍后使用此 run ID 查询候选结果。"));
    } catch (error) { reportError(error, "启动推荐"); }
    finally { button.disabled = false; }
  });
  $("#recommendation-query").addEventListener("submit", async (event) => {
    event.preventDefault();
    const runID = $("#recommendation-run-id").value.trim();
    if (!runID) { showMessage("请输入推荐批次 run ID。", true); return; }
    await loadRecommendations(runID);
  });
  async function loadRecommendations(runID) {
    const list = $("#recommendation-list"); list.replaceChildren(node("p", "empty-cell", "正在读取推荐批次…"));
    try {
      const result = await api(`/admin/api/recommendations?run_id=${encodeURIComponent(runID)}`);
      const run = result.run || {}; renderRunSummary(run);
      list.replaceChildren();
      const items = Array.isArray(result.items) ? result.items : [];
      if (!items.length) {
        list.append(node("p", "empty-cell", run.state === "succeeded" ? "本批次没有待审核候选。" : "暂时还没有候选结果。任务可能仍在运行，请稍后再次查询。"));
        return;
      }
      const cards = items.slice(0, 60).map((item) => {
        const rendered = renderRecommendation(item);
        list.append(rendered.card);
        return { item, samples: rendered.samples };
      });
      // Keep local preview requests bounded while each artist remains capped at four samples.
      let next = 0;
      const workers = Array.from({ length: Math.min(4, cards.length) }, async () => {
        while (next < cards.length) {
          const index = next++;
          await loadRecommendationSamples(cards[index].item, cards[index].samples);
        }
      });
      await Promise.all(workers);
    } catch (error) { list.replaceChildren(node("p", "empty-cell", "无法读取该批次。")); reportError(error, "读取推荐结果"); }
  }
  function renderRunSummary(run) {
    const summary = $("#recommendation-run"); summary.replaceChildren();
    const stateNames = { pending: "等待处理", running: "正在计算", succeeded: "已完成", failed: "失败", paused_auth: "等待凭据" };
    summary.append(node("span", "pill", `批次 ${run.id || "未知"}`));
    summary.append(node("span", "pill", stateNames[run.state] || run.state || "状态未知"));
    summary.append(node("span", "muted", `已使用上游请求 ${Number(run.request_count) || 0}`));
    if (run.budget) {
      const budget = typeof run.budget === "string" ? safeJSON(run.budget) : run.budget;
      if (budget) summary.append(node("span", "muted", `请求上限 ${Number(budget.total_requests) || 300}`));
    }
  }
  function safeJSON(raw) { try { return JSON.parse(raw); } catch (_) { return null; } }
  function renderRecommendation(item) {
    const artist = item.artist || {}; const id = String(artist.id || item.artist_id || "");
    const card = node("article", "recommendation-card"); card.dataset.artistId = id;
    const heading = node("div", "candidate-heading"); const identity = node("div");
    identity.append(node("h3", "candidate-name", artist.name || `Pixiv UID ${id}`));
    identity.append(node("div", "candidate-subtitle", `UID ${id}${artist.account ? ` · ${artist.account}` : ""}`));
    const metrics = node("div", "candidate-metrics");
    const score = Number(item.score);
    metrics.append(node("span", "candidate-score", `启发式排序分 ${Number.isFinite(score) ? score.toFixed(3) : "未知"}`));
    const confidence = String(item.confidence || "low").toLowerCase();
    const confidenceNames = { low: "低置信度", medium: "中等置信度", high: "较高置信度" };
    metrics.append(node("span", `candidate-confidence ${confidence}`, confidenceNames[confidence] || `置信度：${confidence}`));
    heading.append(identity, metrics); card.append(heading);
    const evidence = node("div", "candidate-evidence");
    evidence.append(renderEvidence("推荐依据", item.reasons)); evidence.append(renderEvidence("召回来源", item.sources));
    const componentNames = { tag_match: "标签匹配", source_evidence: "来源证据", quality: "样本质量", negative_tag_penalty: "负向标签惩罚", median_bookmarks: "样本收藏中位数" };
    const componentParts = [];
    if (item.components && typeof item.components === "object") {
      Object.entries(componentNames).forEach(([key, label]) => {
        if (item.components[key] !== undefined && item.components[key] !== null) {
          const value = typeof item.components[key] === "number" ? item.components[key].toFixed(3) : item.components[key];
          componentParts.push(`${label}：${value}`);
        }
      });
      if (Array.isArray(item.components.matched_tags) && item.components.matched_tags.length) componentParts.push(`命中标签：${item.components.matched_tags.join("、")}`);
    }
    evidence.append(renderEvidence(`分数组成${item.score_version ? ` · ${item.score_version}` : ""}`, componentParts));
    card.append(evidence);
    const sampleIDs = Array.isArray(item.sample_illust_ids) ? item.sample_illust_ids.slice(0, 4) : [];
    if (sampleIDs.length) card.append(node("div", "artwork-tags", `候选样本 ID：${sampleIDs.join(" · ")}`));
    const samples = node("div", "candidate-samples"); samples.append(node("span", "artwork-tags", "最多读取 4 个样本预览…")); card.append(samples);
    const actions = node("div", "candidate-actions");
    const state = node("span", "candidate-status", `审核状态：${statusLabel(artist.review_status || "pending")}`); actions.append(state);
    if (!artist.review_status || artist.review_status === "pending") {
      addButton(actions, "批准并排队", "button button-primary button-small", () => recommendationAction(id, "approve", state, actions));
      addButton(actions, "拒绝", "button button-secondary button-small", () => recommendationAction(id, "reject", state, actions));
      addButton(actions, "屏蔽", "button button-quiet button-small", () => recommendationAction(id, "block", state, actions));
    }
    card.append(actions); return { card, samples };
  }
  function renderEvidence(label, values) {
    const block = node("div", "evidence-block"); block.append(node("span", "evidence-label", label));
    const chips = node("div", "evidence-values");
    const items = Array.isArray(values) ? values : [];
    if (!items.length) chips.append(node("span", "evidence-chip", "暂无说明"));
    else items.slice(0, 8).forEach((value) => chips.append(node("span", "evidence-chip", value)));
    block.append(chips); return block;
  }
  async function loadRecommendationSamples(item, container) {
    const artist = item.artist || {}; const artistID = String(artist.id || item.artist_id || "");
    try {
      const query = new URLSearchParams({ artist_id: artistID, limit: "4" });
      const result = await api(`/admin/api/artworks?${query.toString()}`);
      container.replaceChildren();
      const works = Array.isArray(result.items) ? result.items.slice(0, 4) : [];
      if (!works.length) { container.append(node("span", "artwork-tags", "暂无可预览样本")); return; }
      works.forEach((work) => {
        const sample = node("div", "candidate-sample");
        const pages = Array.isArray(work.pages) ? work.pages : [];
        const page = pages.find((candidate) => candidate.media_state === "indexed") || pages[0];
        if (page && Number(page.page_index) >= 0) {
          const image = node("img"); image.loading = "lazy"; image.alt = work.title || `作品 ${work.id}`;
          image.src = `/admin/api/previews/${encodeURIComponent(String(work.id))}/${encodeURIComponent(String(page.page_index))}`;
          image.addEventListener("error", () => { image.removeAttribute("src"); image.alt = "预览暂不可用"; }); sample.append(image);
        } else sample.append(node("div", "artwork-preview", "无预览"));
        sample.append(node("div", "candidate-sample-title", work.title || `作品 ${work.id}`));
        container.append(sample);
      });
    } catch (_) { container.replaceChildren(node("span", "artwork-tags", "样本预览暂不可用")); }
  }
  async function recommendationAction(id, action, state, actions) {
    if (!id) { showMessage("候选画师缺少有效 UID。", true); return; }
    const buttons = actions.querySelectorAll("button"); buttons.forEach((button) => { button.disabled = true; });
    try {
      const result = await api(`/admin/api/artists/${encodeURIComponent(id)}/${action}`, { method: "POST" });
      const nextState = action === "approve" ? "approved" : action === "reject" ? "rejected" : "blocked";
      state.textContent = `审核状态：${statusLabel(nextState)}`;
      buttons.forEach((button) => button.remove());
      if (action === "approve") showMessage(`候选已批准，采集任务 ${result.job_id || "已创建"} 已排队；尚未表示采集完成。`);
      else showMessage(`候选已${action === "reject" ? "拒绝" : "屏蔽"}，未创建采集任务。`);
      await loadArtists(false); await loadJobs();
    } catch (error) { buttons.forEach((button) => { button.disabled = false; }); reportError(error, "审核推荐候选"); }
  }

  async function openArtworks(artistID, artistName) {
    const dialog = $("#artwork-dialog"); const list = $("#artwork-list");
    $("#artwork-heading").textContent = `${artistName} · 作品预览`;
    list.replaceChildren(node("p", "empty-cell", "正在读取作品…")); dialog.showModal();
    try {
      const query = new URLSearchParams({ artist_id: String(artistID), limit: "50" });
      const result = await api(`/admin/api/artworks?${query.toString()}`);
      list.replaceChildren(); const artworks = Array.isArray(result.items) ? result.items : [];
      if (!artworks.length) { list.append(node("p", "empty-cell", "暂无已入库作品样本。")); return; }
      artworks.forEach((artwork) => list.append(renderArtwork(artwork)));
    } catch (error) { list.replaceChildren(node("p", "empty-cell", "无法读取作品，请稍后重试。")); reportError(error, "读取作品"); }
  }
  function renderArtwork(artwork) {
    const card = node("article", "artwork-card"); const pages = Array.isArray(artwork.pages) ? artwork.pages : [];
    const page = pages.find((item) => item.media_state === "indexed") || pages[0];
    if (page && page.page_index >= 0) {
      const image = node("img", "artwork-preview"); image.loading = "lazy"; image.alt = artwork.title || `作品 ${artwork.id}`;
      image.src = `/admin/api/previews/${encodeURIComponent(String(artwork.id))}/${encodeURIComponent(String(page.page_index))}`;
      image.addEventListener("error", () => { image.removeAttribute("src"); image.alt = "预览暂不可用"; }); card.append(image);
    } else card.append(node("div", "artwork-preview", "暂无可用预览"));
    const info = node("div", "artwork-info");
    info.append(node("div", "artwork-title", artwork.title || `作品 ${artwork.id}`));
    info.append(node("div", "artwork-meta", `ID ${artwork.id} · ${artwork.rating || "未知分级"} · ${artwork.ai_status || "AI 未知"}`));
    const tags = Array.isArray(artwork.tags) ? artwork.tags.join(" · ") : "";
    if (tags) info.append(node("div", "artwork-tags", tags));
    const actions = node("div", "preference-actions");
    [["like", "喜欢"], ["dislike", "排除"], ["neutral", "清除偏好"]].forEach(([value, label]) => addButton(actions, label, "button button-secondary", async () => {
      try {
        await api(`/admin/api/preferences/artworks/${encodeURIComponent(String(artwork.id))}`, { method: "PUT", body: JSON.stringify({ value }) });
        showMessage(`作品 ${artwork.id} 的偏好已更新；预取与下载不会自动改变偏好。`);
      } catch (error) { reportError(error, "保存作品偏好"); }
    }));
    info.append(actions); card.append(info); return card;
  }
  $("#close-artworks").addEventListener("click", () => $("#artwork-dialog").close());
  $("#artwork-dialog").addEventListener("click", (event) => { if (event.target === event.currentTarget) event.currentTarget.close(); });

  function checkedValues(name) { return $$(`input[name="${name}"]:checked`).map((input) => input.value); }
  function setChecked(name, values) { const selected = new Set(Array.isArray(values) ? values : []); $$(`input[name="${name}"]`).forEach((input) => { input.checked = selected.has(input.value); }); }
  function parseTagGroups(text) { return text.split(/\r?\n/).map((line) => line.split(",").map((tag) => tag.trim().toLowerCase()).filter(Boolean)).filter((group) => group.length); }
  async function loadPreferences() {
    try {
      const result = await api("/admin/api/settings/preferences"); const rules = result.rules || {};
      preferenceVersion = Number(result.version) || 0;
      setChecked("rating", rules.allowed_ratings); setChecked("ai", rules.allowed_ai);
      $("#min-width").value = String(rules.min_width || 0); $("#min-height").value = String(rules.min_height || 0);
      $("#orientation").value = rules.orientation || "any"; $("#strategy").value = rules.strategy || "balanced";
      $("#include-tags").value = (rules.included_tags || []).map((group) => group.join(", ")).join("\n");
      $("#exclude-tags").value = (rules.excluded_tags || []).join(", ");
      $("#preference-version").textContent = `当前版本 ${preferenceVersion}`;
    } catch (error) { reportError(error, "读取偏好"); }
  }
  $("#preferences-form").addEventListener("submit", async (event) => {
    event.preventDefault();
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
      $("#preference-version").textContent = `当前版本 ${preferenceVersion}`; showMessage("选图偏好已保存；未知分类仍会保持未知并被默认规则排除。");
    } catch (error) { reportError(error, "保存偏好"); }
  });

  async function loadAccount() {
    try {
      const result = await api("/admin/api/account/status");
      const labels = { configured: "已配置，等待认证", ready: "已连接", expired: "凭据失效", error: "状态异常" };
      $("#account-status").textContent = labels[result.status] || result.status || "未知";
    } catch (error) { $("#account-status").textContent = "无法读取"; reportError(error, "读取账号状态"); }
  }
  let pixivLoginTimer;
  let pixivLoginTab = null;
  let pixivLoginDestination = "";
  const pixivLoginLabels = {
    LOGIN_UNAVAILABLE: "浏览器登录未启用，请使用 scripts/dev.sh 启动。",
    LOGIN_DEPENDENCY_UNAVAILABLE: "登录依赖不可用，请重新运行 scripts/dev.sh。",
    LOGIN_BROWSER_UNAVAILABLE: "登录浏览器尚未安装，请重新运行 scripts/dev.sh。",
    LOGIN_DISPLAY_UNAVAILABLE: "登录流程无法启动，请检查服务状态后重试。",
    LOGIN_TIMEOUT: "登录等待已超时，请重试。",
    LOGIN_RATE_LIMITED: "登录尝试过于频繁，请一分钟后重试。",
    CREDENTIAL_SAVE_FAILED: "授权已返回，但保存失败，请检查私密目录权限后重新登录。",
    LOGIN_FAILED: "登录未完成。请检查 Pixiv 页面和网络后重试，也可以使用下方令牌方式。"
  };
  function renderPixivLogin(status) {
    const running = status.state === "running";
    $("#pixiv-login-start").disabled = !status.enabled || running;
    $("#pixiv-login-cancel").hidden = !running;
    const labels = { idle: "准备就绪。点击后在新标签打开 Pixiv 登录。", running: status.auto_callback ? "请完成 Pixiv 登录，并允许浏览器打开 Pixiv Curator Login；授权会自动保存。" : "请在 Pixiv 标签完成登录，再复制 Network 中的授权 code 到此页提交。", succeeded: "登录授权已保存。可以开始同步关注；暂停任务需手动恢复。", canceled: "登录已取消。" };
    $("#pixiv-login-status").textContent = !status.enabled ? pixivLoginLabels.LOGIN_UNAVAILABLE : (pixivLoginLabels[status.error_code] || labels[status.state] || pixivLoginLabels.LOGIN_FAILED);
    const loginURL = validPixivLoginURL(status.login_url);
    const manualLink = $("#pixiv-login-open-link");
    if (running && loginURL) {
      manualLink.href = loginURL;
      manualLink.hidden = false;
      if (pixivLoginDestination !== loginURL) {
        pixivLoginDestination = loginURL;
        try { if (pixivLoginTab && !pixivLoginTab.closed) pixivLoginTab.location.replace(loginURL); }
        catch (_) { /* The manual link remains available if the reserved tab cannot navigate. */ }
      }
    } else {
      manualLink.hidden = true;
      manualLink.removeAttribute("href");
    }
    clearTimeout(pixivLoginTimer);
    if (running) pixivLoginTimer = setTimeout(loadPixivLogin, 1500);
    if (status.state === "succeeded") loadAccount();
  }
  function validPixivLoginURL(raw) {
    if (typeof raw !== "string" || !raw) return "";
    try {
      const url = new URL(raw);
      const allowed = (url.hostname === "app-api.pixiv.net" && url.pathname === "/web/v1/login") ||
        (url.hostname === "accounts.pixiv.net" && url.pathname === "/login");
      if (url.protocol !== "https:" || url.username || url.password || url.port || url.hash || !allowed) return "";
      return url.href;
    } catch (_) { return ""; }
  }
  async function loadPixivLogin() {
    try { renderPixivLogin(await api("/admin/api/account/login")); }
    catch (error) { $("#pixiv-login-status").textContent = "无法读取登录状态，请刷新后重试。"; reportError(error, "读取登录状态"); }
  }
  $("#pixiv-login-start").addEventListener("click", async (event) => {
    // Open synchronously in the user gesture so popup blockers allow this tab.
    pixivLoginTab = window.open("about:blank", "_blank");
    pixivLoginDestination = "";
    if (pixivLoginTab) pixivLoginTab.opener = null;
    event.currentTarget.disabled = true;
    try { renderPixivLogin(await api("/admin/api/account/login", { method: "POST", body: "{}" })); }
    catch (error) {
      await loadPixivLogin();
      showMessage(pixivLoginLabels[error.message] || "无法启动登录，请稍后重试。", true);
    }
  });
  $("#pixiv-login-cancel").addEventListener("click", async () => {
    try { renderPixivLogin(await api("/admin/api/account/login/cancel", { method: "POST" })); }
    catch (error) { reportError(error, "取消登录"); }
  });
  $("#pixiv-login-code-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = $("#pixiv-login-code");
    const code = input.value.trim();
    input.value = "";
    try {
      await api("/admin/api/account/login/code", { method: "POST", body: JSON.stringify({ code }) });
      showMessage("授权 code 已提交，正在等待 Pixiv 授权结果。");
      await loadPixivLogin();
    } catch (error) { reportError(error, "提交授权 code"); }
  });
  $("#reload-account").addEventListener("click", loadAccount);
  $("#credential-form").addEventListener("submit", async (event) => {
    event.preventDefault(); const input = $("#refresh-token"); const refreshToken = input.value; input.value = "";
    try {
      const result = await api("/admin/api/account/credentials", { method: "POST", body: JSON.stringify({ refresh_token: refreshToken }) });
      $("#account-status").textContent = result.status || "已保存"; showMessage("凭据已安全保存，页面不会回显令牌。");
    } catch (error) { reportError(error, "保存凭据"); }
  });
  $("#sync-following").addEventListener("click", async (event) => {
    const button = event.currentTarget; button.disabled = true;
    try {
      const result = await api("/admin/api/following/sync", { method: "POST" });
      showMessage(`关注同步任务 ${result.job_id || "已创建"} 已排队；同步不会批准或采集画师。`); await loadJobs();
    } catch (error) { reportError(error, "同步关注"); }
    finally { button.disabled = false; }
  });
  $("#logout-button").addEventListener("click", async () => {
    try { await api("/admin/logout", { method: "POST" }); window.location.assign("/admin/login"); }
    catch (error) { reportError(error, "退出登录"); }
  });

  loadArtists(false);
})();
