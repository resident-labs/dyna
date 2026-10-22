// Collects the r13 numbers from the shared browser. Started by 'dyna browser trace start'.
// argv: playwright-module endpoint out-dir [url]. SIGTERM writes result.json and exits.
import { rename, writeFile } from "node:fs/promises";
import { attach } from "./browser_page.mjs";

const [module, endpoint, dir, url] = process.argv.slice(2);
const { page } = await attach(module, endpoint);

let requests = 0;
const errors = [];
page.on("request", () => requests++);
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
page.on("pageerror", (e) => errors.push(String(e.message ?? e)));

// The collector lives in this process, so reports survive navigations and origin
// changes: they arrive as CDP binding calls from whichever document is loading.
// Each trace has its own id, so a script a previous trace left on the page, or a
// late report from it, cannot join this result.
const longTasks = [];
const usable = { origin: null, mark: null, dcl: null };
const see = (kind, value, origin) => {
  if (kind === "longtask") return longTasks.push(Math.round(value));
  usable.origin ??= origin;
  if (origin !== usable.origin) return;
  if (kind === "mark") usable.mark ??= value;
  else usable.dcl ??= value;
};

const traceId = `${Date.now()}-${Math.round(Math.random() * 1e9)}`;
const cdp = await page.context().newCDPSession(page);
cdp.on("Runtime.bindingCalled", ({ name, payload }) => {
  if (name !== "__dynaTrace") return;
  let entry;
  try {
    entry = JSON.parse(payload);
  } catch {
    return;
  }
  const [id, kind, value, origin] = entry;
  if (id === traceId) see(kind, value, origin);
});
await cdp.send("Runtime.enable");
await cdp.send("Page.enable");
await cdp.send("Runtime.addBinding", { name: "__dynaTrace" });
// Runs at the start of every document this trace loads, including after a
// navigation to another origin, which a page-scoped store would lose.
const inject = `(() => {
  if (globalThis !== globalThis.top) return;
  const send = (kind, value) => {
    const binding = globalThis.__dynaTrace;
    if (typeof binding !== "function") return;
    try { binding(JSON.stringify([${JSON.stringify(traceId)}, kind, Math.round(value), performance.timeOrigin])); } catch {}
  };
  try {
    new PerformanceObserver((list) => list.getEntries().forEach((e) => send("longtask", e.duration))).observe({ type: "longtask", buffered: true });
    new PerformanceObserver((list) => list.getEntries().forEach((e) => send("mark", e.startTime))).observe({ type: "mark", buffered: true });
  } catch {}
  addEventListener("DOMContentLoaded", () => send("dcl", performance.now()));
})();`;
const injected = await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: inject });
await cdp.send("Performance.enable");
async function heap() {
  await cdp.send("HeapProfiler.collectGarbage").catch(() => {});
  const { metrics } = await cdp.send("Performance.getMetrics");
  return metrics.find((m) => m.name === "JSHeapUsedSize")?.value ?? 0;
}

if (url) await page.goto(url, { waitUntil: "domcontentloaded" });
else await page.reload({ waitUntil: "domcontentloaded" });
await page.waitForLoadState("load").catch(() => {});
const heapStart = await heap();
await writeFile(`${dir}/ready`, "");

process.on("SIGTERM", async () => {
  // Leave the page as it was: this trace's script must not report into a later one.
  await cdp.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: injected.identifier }).catch(() => {});
  await cdp.send("Runtime.removeBinding", { name: "__dynaTrace" }).catch(() => {});
  const heapEnd = await heap();
  const screenshot = `${dir}/trace.png`;
  await page.screenshot({ path: screenshot }).catch(() => {});
  await writeFile(`${dir}/result.tmp`, JSON.stringify({
    url: page.url(),
    time_to_usable_ms: (usable.mark ?? usable.dcl) === null ? null : Math.round(usable.mark ?? usable.dcl),
    time_to_usable_source: usable.mark !== null ? "mark" : "DOMContentLoaded",
    long_tasks_over_200ms: longTasks.filter((d) => d > 200).length,
    heap_growth_bytes: Math.round(heapEnd - heapStart),
    request_count: requests,
    console_errors: errors,
    screenshot,
  }));
  await rename(`${dir}/result.tmp`, `${dir}/result.json`);
  process.exit(0);
});
