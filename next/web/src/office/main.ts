import { parseOfficeParentMessage, type OfficeTheme } from "../lib/office.ts";

const root = document.getElementById("root")!;
const style = document.createElement("style");
style.textContent = `
  :root {
    --bg: #131416; --sidebar: #0c0d0e; --raised: #1b1c1f;
    --line: rgb(255 255 255 / 8%); --firm: rgb(255 255 255 / 14%);
    --selected: rgb(255 255 255 / 9%); --hover: rgb(255 255 255 / 4%);
    --focus: #93a6f5; --text: #e8e9eb; --muted: #9c9fa6;
    --shadow: rgb(0 0 0 / 36%);
    --sans: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
    --mono: ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace;
    font: 14px/1.45 var(--sans);
    -webkit-font-smoothing: antialiased;
  }
  * { box-sizing: border-box; scrollbar-width: thin; scrollbar-color: transparent transparent; }
  :hover { scrollbar-color: color-mix(in srgb, var(--muted) 45%, transparent) transparent; }
  html, body { margin: 0; width: 100%; height: 100%; overflow: hidden; background: var(--sidebar); color: var(--text); }
  #root { width: 100%; height: 100%; display: flex; flex-direction: column; min-height: 0; }
  button { font: inherit; color: inherit; cursor: pointer; }
  button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }

  /* Shared bar for sheet tabs / zoom controls */
  .bar {
    flex: none; display: flex; align-items: center; gap: 4px;
    min-height: 38px; padding: 4px 12px;
    border-bottom: 1px solid var(--line); background: var(--raised);
    overflow-x: auto; white-space: nowrap;
  }
  .bar.bottom { border-bottom: 0; border-top: 1px solid var(--line); }
  .tab {
    flex: none; height: 28px; padding: 0 10px; border: 0; border-radius: 6px;
    background: none; color: var(--muted); font-size: 12.5px; font-weight: 500;
  }
  .tab:hover { background: var(--hover); color: var(--text); }
  .tab[aria-selected="true"] { background: var(--selected); color: var(--text); }
  .spacer { flex: 1; }
  .status { flex: none; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  .zoom-group { flex: none; display: inline-flex; align-items: center; gap: 2px; }
  .zoom-btn {
    min-width: 28px; height: 28px; padding: 0 7px; border: 0; border-radius: 6px;
    background: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums;
  }
  .zoom-btn:hover:enabled { background: var(--hover); color: var(--text); }
  .zoom-btn:disabled { opacity: 0.45; cursor: default; }
  .note {
    flex: none; margin: 0; padding: 8px 16px;
    border-bottom: 1px solid var(--line); background: var(--raised);
    color: var(--muted); font-size: 12.5px;
  }

  /* Word document stage */
  .doc-scroll {
    flex: 1; min-height: 0; overflow: auto;
    padding: 20px 16px 32px; background: var(--sidebar);
    -webkit-overflow-scrolling: touch;
  }
  .doc-zoom-box { margin: 0 auto; transform-origin: top left; }
  .doc-host .docx-wrapper {
    background: transparent !important;
    padding: 0 !important;
    display: flex; flex-direction: column; align-items: center; gap: 16px;
  }
  .doc-host .docx-wrapper > section.docx {
    background: #ffffff;
    color: #1a1c20;
    box-shadow: 0 4px 20px var(--shadow) !important;
    border-radius: 2px;
    margin-bottom: 0 !important;
  }
  .doc-host img { max-width: 100%; height: auto; }

  /* Spreadsheet view (mirrors Dyna's MediaText table view) */
  .sheet-scroll {
    flex: 1; min-height: 0; overflow: auto; background: var(--bg);
    -webkit-overflow-scrolling: touch;
  }
  .sheet-table {
    width: max-content; min-width: 100%;
    border-collapse: separate; border-spacing: 0;
    font-size: 13px; font-variant-numeric: tabular-nums;
  }
  .sheet-table th, .sheet-table td {
    max-width: 40ch; padding: 6px 12px;
    border-bottom: 1px solid var(--line); border-right: 1px solid var(--line);
    text-align: left; vertical-align: top; overflow-wrap: anywhere;
  }
  .sheet-table th:last-child, .sheet-table td:last-child { border-right: 0; }
  .sheet-table thead th {
    position: sticky; top: 0; z-index: 2;
    background: var(--raised); color: var(--muted);
    font-size: 11.5px; font-weight: 600; white-space: nowrap; text-align: center;
  }
  .sheet-table tbody th.row-num {
    position: sticky; left: 0; z-index: 1;
    width: 1%; padding: 6px 10px;
    background: var(--raised); color: var(--muted);
    font-size: 11.5px; font-weight: 500; text-align: right; white-space: nowrap;
  }
  .sheet-table thead th.corner { left: 0; z-index: 3; }
  .sheet-table tbody tr:hover td { background: var(--hover); }
  .empty { padding: 24px; color: var(--muted); font-size: 13px; text-align: center; }

  /* Slides view */
  .slides-scroll {
    flex: 1; min-height: 0; overflow: auto;
    padding: 20px 16px 32px; background: var(--sidebar);
    -webkit-overflow-scrolling: touch;
  }
  .slides-stack {
    display: flex; flex-direction: column; align-items: center; gap: 20px;
    margin: 0 auto;
  }
  .slide-card {
    display: flex; flex-direction: column; align-items: center; gap: 6px;
    width: 100%;
  }
  .slide-frame {
    position: relative; width: 100%;
    background: #ffffff; border-radius: 4px; overflow: hidden;
    box-shadow: 0 4px 20px var(--shadow);
  }
  .slide-frame canvas { display: block; width: 100%; height: auto; }
  .slide-label { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }

  @media (max-width: 640px) {
    .doc-scroll, .slides-scroll { padding: 12px 10px 24px; }
    .bar { padding: 4px 8px; }
    .tab, .zoom-btn { min-height: 34px; }
  }
`;
document.head.appendChild(style);

let cleanup: (() => void) | undefined;
let activeId = 0;

function applyTheme(theme: OfficeTheme) {
  const el = document.documentElement;
  el.style.colorScheme = theme.mode;
  el.style.setProperty("--bg", theme.bg);
  el.style.setProperty("--sidebar", theme.sidebar);
  el.style.setProperty("--raised", theme.raised);
  el.style.setProperty("--line", theme.line);
  el.style.setProperty("--firm", theme.firm);
  el.style.setProperty("--selected", theme.selected);
  el.style.setProperty("--hover", theme.hover);
  el.style.setProperty("--focus", theme.focus);
  el.style.setProperty("--text", theme.text);
  el.style.setProperty("--muted", theme.muted);
  el.style.setProperty("--shadow", theme.shadow);
}

// Let Escape close the parent MediaViewer modal even when focus is inside the sandboxed frame.
window.addEventListener("keydown", event => {
  if (event.key === "Escape") window.parent.postMessage({ type: "office:close" }, "*");
});

// Intercept every link click inside rendered documents. Only http(s) links are forwarded to the parent.
document.addEventListener("click", event => {
  const target = event.target instanceof Element ? event.target.closest("a[href]") : null;
  if (!(target instanceof HTMLAnchorElement)) return;
  event.preventDefault();
  event.stopPropagation();
  const href = target.getAttribute("href")?.trim() ?? "";
  if (!href) return;
  if (href.startsWith("#")) {
    const id = href.slice(1);
    const anchor = id ? document.getElementById(id) : null;
    anchor?.scrollIntoView({ behavior: "smooth" });
    return;
  }
  let parsed: URL;
  try { parsed = new URL(href); } catch { return; }
  if (parsed.protocol === "http:" || parsed.protocol === "https:") {
    window.parent.postMessage({ type: "office:open", url: parsed.href }, "*");
  }
}, true);

// Neutralize anchor attributes inside rendered DOM so middle-click or right-click cannot navigate the sandbox frame.
function stripUnsafeLinks(container: ParentNode) {
  for (const anchor of container.querySelectorAll("a")) {
    const href = anchor.getAttribute("href")?.trim() ?? "";
    anchor.removeAttribute("target");
    anchor.setAttribute("rel", "noopener noreferrer");
    if (href.startsWith("#")) continue;
    let safe = false;
    try {
      const parsed = new URL(href);
      safe = parsed.protocol === "http:" || parsed.protocol === "https:";
    } catch { safe = false; }
    if (!safe) anchor.removeAttribute("href");
  }
}

function makeZoomBar(initialScale: number, onScale: (scale: number) => void, extra?: HTMLElement): { bar: HTMLElement; setLabel: (text: string) => void; sync: (scale: number, text?: string) => void } {
  let scale = initialScale;
  const bar = document.createElement("div");
  bar.className = "bar";
  const label = document.createElement("span");
  label.className = "status";
  const spacer = document.createElement("span");
  spacer.className = "spacer";
  const group = document.createElement("div");
  group.className = "zoom-group";
  group.setAttribute("role", "group");
  group.setAttribute("aria-label", "Zoom");

  const outBtn = document.createElement("button");
  outBtn.type = "button"; outBtn.className = "zoom-btn"; outBtn.textContent = "−"; outBtn.setAttribute("aria-label", "Zoom out");
  const fitBtn = document.createElement("button");
  fitBtn.type = "button"; fitBtn.className = "zoom-btn"; fitBtn.textContent = "Fit"; fitBtn.setAttribute("aria-label", "Fit width");
  const inBtn = document.createElement("button");
  inBtn.type = "button"; inBtn.className = "zoom-btn"; inBtn.textContent = "+"; inBtn.setAttribute("aria-label", "Zoom in");

  const sync = (next: number, text?: string) => {
    scale = Math.min(2.5, Math.max(0.25, Math.round(next * 100) / 100));
    fitBtn.textContent = text ?? `${Math.round(scale * 100)}%`;
    outBtn.disabled = scale <= 0.25;
    inBtn.disabled = scale >= 2.5;
  };
  outBtn.onclick = () => onScale(Math.max(0.25, scale / 1.2));
  inBtn.onclick = () => onScale(Math.min(2.5, scale * 1.2));
  fitBtn.onclick = () => onScale(0); // 0 signals refit
  group.append(outBtn, fitBtn, inBtn);
  if (extra) bar.append(extra);
  bar.append(label, spacer, group);
  return {
    bar,
    setLabel: text => { label.textContent = text; },
    sync,
  };
}

async function renderWord(buffer: ArrayBuffer, name: string): Promise<() => void> {
  const { renderAsync } = await import("docx-preview");
  const host = document.createElement("div");
  host.className = "doc-host";
  const styleHost = document.createElement("div");
  const scroll = document.createElement("div");
  scroll.className = "doc-scroll";
  scroll.tabIndex = 0;
  scroll.setAttribute("role", "region");
  scroll.setAttribute("aria-label", name);
  const zoomOuter = document.createElement("div");
  const zoomBox = document.createElement("div");
  zoomBox.className = "doc-zoom-box";
  zoomBox.append(styleHost, host);
  zoomOuter.append(zoomBox);
  scroll.append(zoomOuter);

  let mode: "fit" | "manual" = "fit";
  let manualScale = 1;
  let naturalWidth = 0;
  let naturalHeight = 0;

  const measure = () => {
    const pages = host.querySelectorAll<HTMLElement>("section.docx");
    let maxW = 0;
    for (const page of pages) maxW = Math.max(maxW, page.offsetWidth);
    naturalWidth = maxW || host.scrollWidth || 816;
    naturalHeight = host.scrollHeight;
  };

  const applyScale = (requested?: number) => {
    if (requested === 0) {
      mode = "fit";
    } else if (requested !== undefined) {
      mode = "manual";
      manualScale = requested;
    }
    const available = Math.max(200, scroll.clientWidth - (window.innerWidth <= 640 ? 20 : 32));
    const fitScale = naturalWidth > available ? available / naturalWidth : 1;
    const scale = mode === "fit" ? fitScale : manualScale;
    zoomBox.style.width = `${naturalWidth}px`;
    zoomBox.style.transform = scale === 1 ? "" : `scale(${scale})`;
    zoomOuter.style.width = `${Math.ceil(naturalWidth * scale)}px`;
    zoomOuter.style.height = `${Math.ceil(naturalHeight * scale)}px`;
    zoomOuter.style.margin = "0 auto";
    sync(scale, mode === "fit" && scale < 0.99 ? `Fit (${Math.round(scale * 100)}%)` : `${Math.round(scale * 100)}%`);
  };

  const { bar, setLabel, sync } = makeZoomBar(1, requested => {
    applyScale(requested);
  });
  root.replaceChildren(bar, scroll);

  await renderAsync(buffer, host, styleHost, {
    inWrapper: true,
    ignoreWidth: false,
    ignoreHeight: false,
    breakPages: true,
    ignoreLastRenderedPageBreak: true,
    renderHeaders: true,
    renderFooters: true,
    renderFootnotes: true,
    renderEndnotes: true,
    useBase64URL: false,
  });

  stripUnsafeLinks(host);
  const pageCount = host.querySelectorAll("section.docx").length;
  setLabel(pageCount === 1 ? "1 page" : `${pageCount} pages`);

  const refresh = () => {
    measure();
    applyScale();
  };
  refresh();
  // Re-measure once images finish decoding so scaled container height matches.
  for (const img of host.querySelectorAll("img")) {
    if (!img.complete) img.addEventListener("load", refresh, { once: true });
  }
  const ro = new ResizeObserver(refresh);
  ro.observe(scroll);
  return () => { ro.disconnect(); };
}

const MAX_SHEET_ROWS = 1000;
const MAX_SHEET_COLS = 50;
const MAX_CELL_LEN = 2000;

function columnLabel(index: number): string {
  let n = index + 1;
  let out = "";
  while (n > 0) {
    const rem = (n - 1) % 26;
    out = String.fromCharCode(65 + rem) + out;
    n = Math.floor((n - 1) / 26);
  }
  return out;
}

async function renderSpreadsheet(buffer: ArrayBuffer, name: string): Promise<() => void> {
  const XLSX = await import("xlsx");
  const workbook = XLSX.read(new Uint8Array(buffer), { type: "array", cellDates: true });
  if (!workbook.SheetNames.length) throw new Error("Workbook has no sheets");

  const bar = document.createElement("div");
  bar.className = "bar";
  bar.setAttribute("role", "tablist");
  bar.setAttribute("aria-label", "Sheets");

  const note = document.createElement("p");
  note.className = "note";
  note.setAttribute("role", "status");
  note.hidden = true;

  const scroll = document.createElement("div");
  scroll.className = "sheet-scroll";
  scroll.tabIndex = 0;
  scroll.setAttribute("role", "region");
  scroll.setAttribute("aria-label", `${name} spreadsheet`);

  let selected = 0;
  const tabs: HTMLButtonElement[] = [];

  const showSheet = (index: number) => {
    selected = index;
    tabs.forEach((tab, i) => tab.setAttribute("aria-selected", i === index ? "true" : "false"));
    const sheetName = workbook.SheetNames[index];
    const sheet = workbook.Sheets[sheetName];
    const raw = XLSX.utils.sheet_to_json<unknown[]>(sheet, { header: 1, raw: false, defval: "" });

    const totalRows = raw.length;
    const moreRows = totalRows > MAX_SHEET_ROWS;
    const rows = moreRows ? raw.slice(0, MAX_SHEET_ROWS) : raw;
    let width = 0;
    let clipped = false;
    for (const row of rows) {
      if (row.length > MAX_SHEET_COLS) { width = MAX_SHEET_COLS; clipped = true; }
      else if (row.length > width) width = row.length;
    }

    const parts: string[] = [];
    if (moreRows) parts.push(`Showing the first ${MAX_SHEET_ROWS.toLocaleString("en-US")} of ${totalRows.toLocaleString("en-US")} rows.`);
    if (clipped) parts.push(`Columns beyond ${MAX_SHEET_COLS} or very long cells are cut.`);
    if (parts.length) {
      note.textContent = `${parts.join(" ")} Download the file for the rest.`;
      note.hidden = false;
    } else {
      note.hidden = true;
    }

    if (!rows.length || width === 0) {
      const empty = document.createElement("div");
      empty.className = "empty";
      empty.textContent = "This sheet is empty.";
      scroll.replaceChildren(empty);
      return;
    }

    const table = document.createElement("table");
    table.className = "sheet-table";
    const thead = document.createElement("thead");
    const headRow = document.createElement("tr");
    const corner = document.createElement("th");
    corner.className = "corner";
    corner.scope = "col";
    headRow.append(corner);
    for (let c = 0; c < width; c++) {
      const th = document.createElement("th");
      th.scope = "col";
      th.textContent = columnLabel(c);
      headRow.append(th);
    }
    thead.append(headRow);

    const tbody = document.createElement("tbody");
    for (let r = 0; r < rows.length; r++) {
      const tr = document.createElement("tr");
      const num = document.createElement("th");
      num.className = "row-num";
      num.scope = "row";
      num.textContent = String(r + 1);
      tr.append(num);
      const line = rows[r];
      for (let c = 0; c < width; c++) {
        const td = document.createElement("td");
        let text = line[c] == null ? "" : String(line[c]);
        if (text.length > MAX_CELL_LEN) {
          text = text.slice(0, MAX_CELL_LEN);
          clipped = true;
        }
        td.textContent = text;
        tr.append(td);
      }
      tbody.append(tr);
    }
    if (clipped && note.hidden) {
      note.textContent = "Some long cells are cut. Download the file for the rest.";
      note.hidden = false;
    }
    table.append(thead, tbody);
    scroll.replaceChildren(table);
    scroll.scrollTop = 0;
    scroll.scrollLeft = 0;
  };

  workbook.SheetNames.forEach((sheetName, index) => {
    const tab = document.createElement("button");
    tab.type = "button";
    tab.className = "tab";
    tab.setAttribute("role", "tab");
    tab.textContent = sheetName;
    tab.onclick = () => showSheet(index);
    tabs.push(tab);
    bar.append(tab);
  });

  root.replaceChildren(bar, note, scroll);
  showSheet(selected);
  return () => {};
}

async function renderSlides(buffer: ArrayBuffer, name: string): Promise<() => void> {
  const mod = await import("pptxviewjs");
  const PPTXViewer = mod.PPTXViewer ?? mod.default?.PPTXViewer;
  if (!PPTXViewer) throw new Error("PPTXViewer unavailable");

  const viewer = new PPTXViewer({ slideSizeMode: "fit", enableThumbnails: false, autoRenderFirstSlide: false });
  await viewer.loadFile(buffer);
  const count = viewer.getSlideCount();
  if (count <= 0) {
    viewer.destroy();
    throw new Error("Presentation has no slides");
  }

  // Extract slide aspect ratio from the loaded presentation if present (default 16:9).
  const dims = (viewer as unknown as { presentation?: { slideSize?: { cx?: number; cy?: number } } }).presentation?.slideSize;
  const aspect = dims?.cx && dims?.cy && dims.cx > 0 && dims.cy > 0 ? dims.cx / dims.cy : 16 / 9;
  const baseWidth = 1280;
  const baseHeight = Math.round(baseWidth / aspect);

  let zoom = 1;
  const scroll = document.createElement("div");
  scroll.className = "slides-scroll";
  scroll.tabIndex = 0;
  scroll.setAttribute("role", "region");
  scroll.setAttribute("aria-label", name);
  const stack = document.createElement("div");
  stack.className = "slides-stack";
  scroll.append(stack);

  const applySlideWidth = (requested?: number) => {
    if (requested === 0) zoom = 1;
    else if (requested !== undefined) zoom = Math.min(2.5, Math.max(0.25, Math.round(requested * 100) / 100));
    const available = Math.max(240, scroll.clientWidth - (window.innerWidth <= 640 ? 20 : 32));
    const fitWidth = Math.min(960, available);
    stack.style.width = `${Math.round(fitWidth * zoom)}px`;
    sync(zoom, zoom === 1 ? "Fit" : `${Math.round(zoom * 100)}%`);
  };

  const { bar, setLabel, sync } = makeZoomBar(1, requested => applySlideWidth(requested));
  setLabel(count === 1 ? "1 slide" : `${count} slides`);
  root.replaceChildren(bar, scroll);
  applySlideWidth();

  const ro = new ResizeObserver(() => applySlideWidth());
  ro.observe(scroll);

  let destroyed = false;
  for (let i = 0; i < count; i++) {
    if (destroyed) break;
    const card = document.createElement("div");
    card.className = "slide-card";
    const frame = document.createElement("div");
    frame.className = "slide-frame";
    frame.style.aspectRatio = `${baseWidth} / ${baseHeight}`;
    const canvas = document.createElement("canvas");
    canvas.width = baseWidth;
    canvas.height = baseHeight;
    canvas.setAttribute("aria-label", `Slide ${i + 1} of ${count}`);
    frame.append(canvas);
    const caption = document.createElement("span");
    caption.className = "slide-label";
    caption.textContent = `Slide ${i + 1} of ${count}`;
    card.append(frame, caption);
    stack.append(card);
    await viewer.renderSlide(i, canvas);
  }

  return () => {
    destroyed = true;
    ro.disconnect();
    viewer.destroy();
  };
}

window.addEventListener("message", async event => {
  if (event.source !== window.parent) return;
  const message = parseOfficeParentMessage(event.data);
  if (!message) return;
  applyTheme(message.theme);
  if (message.type === "office:theme") return;

  activeId = message.id;
  cleanup?.();
  cleanup = undefined;
  root.replaceChildren();

  try {
    let dispose: () => void;
    if (message.kind === "word") dispose = await renderWord(message.buffer, message.name);
    else if (message.kind === "spreadsheet") dispose = await renderSpreadsheet(message.buffer, message.name);
    else dispose = await renderSlides(message.buffer, message.name);
    if (activeId !== message.id) {
      dispose();
      return;
    }
    cleanup = dispose;
    window.parent.postMessage({ type: "office:rendered", id: message.id }, "*");
  } catch {
    if (activeId === message.id) {
      window.parent.postMessage({ type: "office:error", id: message.id }, "*");
    }
  }
});

window.parent.postMessage({ type: "office:ready" }, "*");
