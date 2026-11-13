(() => {
  "use strict";

  const ratings = { safe: "全年龄", r18: "R18", r18g: "R18", unknown: "分级未知" };
  const aiStatuses = { non_ai: "非 AI", ai: "AI", unknown: "AI 状态未知" };
  let dialog = null;
  let state = null;
  let imageGeneration = 0;
  let image;
  let status;
  let dimensions;
  let pageStatus;
  let previousButton;
  let nextButton;
  let pixivLink;
  let imageLink;
  let imageInput;
  let titleNode;
  let artistNode;
  let tagsNode;
  let collectionButton;
  let collectionStatus;
  let collectionAPI = null;

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function positiveID(value) {
    const text = String(value ?? "");
    if (!/^[1-9][0-9]*$/.test(text)) return null;
    const number = Number(text);
    return Number.isSafeInteger(number) && number > 0 ? text : null;
  }

  function ensureDialog() {
    if (dialog) return;
    dialog = element("dialog", "pixiv-viewer-dialog");
    dialog.setAttribute("aria-labelledby", "pixiv-viewer-title");
    const shell = element("section", "pixiv-viewer-shell");
    const header = element("header", "pixiv-viewer-header");
    const heading = element("div", "pixiv-viewer-heading");
    heading.append(element("p", "pixiv-viewer-eyebrow", "作品原图"));
    titleNode = element("h2", "pixiv-viewer-title");
    titleNode.id = "pixiv-viewer-title";
    artistNode = element("p", "pixiv-viewer-artist");
    heading.append(titleNode, artistNode);
    const closeButton = element("button", "pixiv-viewer-close", "关闭");
    closeButton.type = "button";
    closeButton.setAttribute("aria-label", "关闭大图");
    closeButton.addEventListener("click", () => closeViewer());
    const headerActions = element("div", "pixiv-viewer-actions");
    collectionButton = element("button", "pixiv-viewer-page-button pixiv-viewer-collection", "读取采集状态…");
    collectionButton.type = "button";
    collectionButton.hidden = true;
    collectionButton.addEventListener("click", toggleCollection);
    headerActions.append(collectionButton, closeButton);
    header.append(heading, headerActions);
    collectionStatus = element("p", "pixiv-viewer-collection-status");
    collectionStatus.setAttribute("role", "status");
    collectionStatus.hidden = true;

    const metadata = element("div", "pixiv-viewer-metadata");
    const classification = element("div", "pixiv-viewer-classification");
    metadata.append(classification);
    tagsNode = element("div", "pixiv-viewer-tags");
    metadata.append(tagsNode);

    const frame = element("div", "pixiv-viewer-frame");
    image = document.createElement("img");
    image.className = "pixiv-viewer-image";
    image.alt = "Pixiv 作品原图";
    image.decoding = "async";
    image.hidden = true;
    status = element("p", "pixiv-viewer-status", "尚未加载图片");
    dimensions = element("p", "pixiv-viewer-dimensions", "");
    const retryButton = element("button", "pixiv-viewer-retry", "重新加载");
    retryButton.type = "button";
    retryButton.hidden = true;
    retryButton.addEventListener("click", () => loadImage());
    frame.append(image, status, dimensions, retryButton);

    const footer = element("footer", "pixiv-viewer-footer");
    const pager = element("div", "pixiv-viewer-pager");
    previousButton = element("button", "pixiv-viewer-page-button", "上一页");
    previousButton.type = "button";
    previousButton.addEventListener("click", () => changePage(-1));
    pageStatus = element("span", "pixiv-viewer-page-status", "");
    nextButton = element("button", "pixiv-viewer-page-button", "下一页");
    nextButton.type = "button";
    nextButton.addEventListener("click", () => changePage(1));
    pager.append(previousButton, pageStatus, nextButton);

    const links = element("div", "pixiv-viewer-links");
    pixivLink = element("a", "pixiv-viewer-link", "在 Pixiv 打开作品");
    pixivLink.target = "_blank";
    pixivLink.rel = "noopener noreferrer";
    imageLink = element("a", "pixiv-viewer-link", "打开固定图片链接");
    imageLink.target = "_blank";
    imageLink.rel = "noopener noreferrer";
    imageInput = document.createElement("input");
    imageInput.className = "pixiv-viewer-url";
    imageInput.type = "text";
    imageInput.readOnly = true;
    imageInput.setAttribute("aria-label", "固定图片链接");
    links.append(pixivLink, imageLink, imageInput);
    footer.append(pager, links);

    shell.append(header, collectionStatus, metadata, frame, footer);
    dialog.append(shell);
    dialog.addEventListener("click", (event) => {
      if (event.target !== dialog) return;
      const bounds = dialog.getBoundingClientRect();
      const outside = event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom;
      if (outside) closeViewer();
    });
    dialog.addEventListener("cancel", () => stopImage());
    dialog.addEventListener("close", () => {
      stopImage();
      state = null;
    });
    document.body.append(dialog);
  }

  async function readCollection() {
    const current = state;
    collectionStatus.hidden = true;
    collectionButton.hidden = !current?.artistId || !collectionAPI;
    if (collectionButton.hidden) return;
    collectionButton.disabled = true;
    collectionButton.textContent = "读取采集状态…";
    try {
      const artist = await collectionAPI.loadArtist(current.artistId);
      if (state !== current) return;
      current.artist = artist;
      renderCollection();
    } catch (_) {
      if (state !== current) return;
      collectionButton.textContent = "采集状态暂不可用";
    }
  }
  function renderCollection() {
    if (!state?.artist) return;
    collectionButton.disabled = state.artist.review_status !== "approved";
    collectionButton.textContent = collectionButton.disabled ? "作者尚未加入图库" : state.artist.collection_enabled ? "暂停该作者采集" : "恢复该作者采集";
    collectionButton.setAttribute("aria-pressed", String(!!state.artist.collection_enabled));
  }
  async function toggleCollection() {
    const current = state;
    if (!current?.artist || collectionButton.disabled) return;
    const enabled = !current.artist.collection_enabled;
    collectionButton.disabled = true;
    collectionStatus.hidden = false;
    collectionStatus.textContent = "正在保存…";
    try {
      await collectionAPI.setCollection(current.artistId, enabled);
      if (state !== current) return;
      current.artist.collection_enabled = enabled;
      collectionStatus.textContent = enabled ? "已恢复该作者采集" : "已暂停该作者采集";
      collectionAPI.onChange?.(current.artist);
    } catch (_) {
      if (state !== current) return;
      collectionStatus.textContent = "保存失败，请重试。";
    } finally { if (state === current) renderCollection(); }
  }

  function previewURL() {
    return `/admin/api/previews/${state.id}/${state.pageIndex}?size=original`;
  }

  function stopImage() {
    imageGeneration++;
    if (!image) return;
    image.onload = null;
    image.onerror = null;
    image.removeAttribute("src");
    image.hidden = true;
  }

  function loadImage() {
    if (!state || !dialog?.open) return;
    stopImage();
    const generation = imageGeneration;
    const url = previewURL();
    image.alt = `${state.title || `作品 ${state.id}`} 原图`;
    image.hidden = true;
    dimensions.textContent = "";
    status.hidden = false;
    status.textContent = "正在读取原图…";
    const retryButton = dialog.querySelector(".pixiv-viewer-retry");
    retryButton.hidden = true;
    image.onload = () => {
      if (generation !== imageGeneration || !state || !dialog.open) return;
      image.hidden = false;
      dimensions.textContent = `${image.naturalWidth} × ${image.naturalHeight}`;
      status.hidden = true;
    };
    image.onerror = () => {
      if (generation !== imageGeneration || !state || !dialog.open) return;
      image.hidden = true;
      dimensions.textContent = "";
      status.hidden = false;
      status.textContent = "原图读取失败；当前作品与图片链接仍保留。";
      retryButton.hidden = false;
    };
    image.src = url;
    imageInput.value = `${location.origin}${url}`;
    imageLink.href = url;
  }

  function render() {
    titleNode.textContent = state.title || `作品 ${state.id}`;
    artistNode.textContent = state.artistName || "";
    pixivLink.href = `https://www.pixiv.net/artworks/${state.id}`;
    const metadata = dialog.querySelector(".pixiv-viewer-classification");
    metadata.replaceChildren(
      element("span", "pixiv-viewer-chip", `ID ${state.id}`),
      element("span", "pixiv-viewer-chip", ratings[state.rating] || "分级未知"),
      element("span", "pixiv-viewer-chip", aiStatuses[state.aiStatus] || "AI 状态未知")
    );
    tagsNode.replaceChildren();
    const tags = Array.isArray(state.tags) ? state.tags : [];
    if (tags.length) {
      for (const tag of tags) tagsNode.append(element("span", "pixiv-viewer-tag", String(tag)));
    } else {
      tagsNode.append(element("span", "pixiv-viewer-empty-tags", "暂无标签"));
    }
    pageStatus.textContent = `${state.pageIndex + 1} / ${state.pageCount}`;
    previousButton.disabled = state.pageIndex <= 0;
    nextButton.disabled = state.pageIndex >= state.pageCount - 1;
  }

  function changePage(delta) {
    if (!state) return;
    const next = state.pageIndex + delta;
    if (next < 0 || next >= state.pageCount) return;
    state.pageIndex = next;
    render();
    loadImage();
  }

  function closeViewer() {
    if (!dialog) return;
    stopImage();
    if (dialog.open) dialog.close();
    else state = null;
  }

  function open(input) {
    if (!input || typeof input !== "object") return false;
    const id = positiveID(input.id);
    const pageIndex = input.pageIndex;
    if (!id || !Number.isSafeInteger(pageIndex) || pageIndex < 0) return false;
    const rawCount = Number(input.pageCount);
    const pageCount = Number.isSafeInteger(rawCount) && rawCount > 0 ? rawCount : 1;
    if (pageIndex >= pageCount) return false;

    ensureDialog();
    stopImage();
    state = {
      id,
      artistId: positiveID(input.artistId),
      pageIndex,
      pageCount,
      title: String(input.title ?? ""),
      artistName: String(input.artistName ?? ""),
      rating: String(input.rating ?? "unknown"),
      aiStatus: String(input.aiStatus ?? "unknown"),
      tags: Array.isArray(input.tags) ? input.tags : []
    };
    render();
    if (!dialog.open) dialog.showModal();
    loadImage();
    readCollection();
    return true;
  }

  window.PixivCuratorViewer = Object.freeze({ open, close: closeViewer, configure: (api) => { collectionAPI = api; } });
})();
