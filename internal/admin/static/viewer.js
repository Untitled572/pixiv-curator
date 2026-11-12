(() => {
  "use strict";

  const ratings = { safe: "全年龄", r18: "R-18", r18g: "R-18G", unknown: "分级未知" };
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
    header.append(heading, closeButton);

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

    shell.append(header, metadata, frame, footer);
    dialog.append(shell);
    dialog.addEventListener("cancel", () => stopImage());
    dialog.addEventListener("close", () => {
      stopImage();
      state = null;
    });
    document.body.append(dialog);
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
    return true;
  }

  window.PixivCuratorViewer = Object.freeze({ open, close: closeViewer });
})();