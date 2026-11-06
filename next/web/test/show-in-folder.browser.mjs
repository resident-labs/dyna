// Build first, then: node test/show-in-folder.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Real built App, with deterministic API fixtures and enough rows to require virtual scrolling.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/design/built/room-files";
await mkdir(output, { recursive: true });
const dist = fileURLToPath(new URL("../dist/", import.meta.url));
const at = "2026-11-06T10:00:00Z", room = "folder-scroll";
const target = "note-040.md";
const markdown = "# Scroll target\n\nShow in folder should reveal this file in the phone sheet.\n";
const entries = Array.from({ length: 100 }, (_, i) => ({ name: `note-${String(i).padStart(3, "0")}.md`, dir: false, size: Buffer.byteLength(markdown), modified: at }));
function answer(method) {
  switch (method) {
    case "room.list": return { rooms: [{ id: room, name: "Room files", paused: false, created: at, lastRead: 1, waiting: [], questions: [], working: [], failed: 0, hostless: 0 }], seq: 1 };
    case "device.scopes": return { scopes: ["read", "operate", "admin"] };
    case "session.list": return { sessions: [{ id: "root", name: "root", parent: "", profile: "sol", box: "root-box", boxState: "ready", computer: "stopped", goal: "", lastRead: 1, hadJobs: false, startedAt: at }], seq: 1 };
    case "events.list": return { events: [{ seq: 1, id: "file-post", at, room, type: "post.created", from: { kind: "owner" }, body: { home: { kind: "feed" }, text: `Open \`/box/shared/${target}\`, then show it in its folder.` } }], names: {} };
    case "workflow.list": return { runs: [] };
    case "op.list": return { operations: [] };
    case "question.list": return { questions: [] };
    case "goal.list": return { goals: [] };
    case "pr.list": return { prs: [] };
    case "host.list": return { hosts: [] };
    case "accounts.list": case "accounts.quota": return { accounts: [] };
    default: return {};
  }
}
const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, "http://fixture");
  if (url.pathname === "/v1/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.write('event: hello\ndata: {"build":"show-in-folder-fixture"}\n\n');
    return;
  }
  if (url.pathname === `/v1/rooms/${room}/shared/tree`) {
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify({ entries }));
    return;
  }
  if (url.pathname === `/v1/rooms/${room}/shared/file`) {
    response.writeHead(200, { "Content-Type": "text/markdown", "Content-Length": Buffer.byteLength(markdown), "Last-Modified": new Date(at).toUTCString() });
    response.end(markdown);
    return;
  }
  if (url.pathname.startsWith("/v1/")) {
    for await (const chunk of request) { /* consume the request */ }
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify(answer(url.pathname.slice(4))));
    return;
  }
  const relative = url.pathname.slice(1);
  const file = relative.startsWith("assets/") ? path.join(dist, relative) : path.join(dist, "index.html");
  const mime = { ".js": "text/javascript", ".css": "text/css", ".html": "text/html", ".woff2": "font/woff2" }[path.extname(file)] ?? "application/octet-stream";
  response.writeHead(200, { "Content-Type": mime });
  response.end(await readFile(file));
});
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
try {
  for (const width of [390, 1440]) {
    const context = await browser.newContext({ viewport: { width, height: 1000 }, hasTouch: width === 390 });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", error => errors.push(String(error)));
    await page.goto(`${base}/r/${room}`);
    const link = page.locator(`a[data-shared-path="${target}"]`).first();
    await link.waitFor();
    const panel = page.locator("aside.members");
    // Cache the tree, then hide the phone sheet before opening the linked file.
    if (width === 390) await page.locator("button.panel-toggle").click();
    await panel.getByRole("button", { name: "Files", exact: true }).click();
    const scroll = page.locator(".files-tree .virtual");
    await page.locator('button[data-shared-row="note-000.md"]').waitFor();
    assert.equal(await page.locator(`button[data-shared-row="${target}"]`).count(), 0, "target starts outside virtual window");
    if (width === 390) await panel.press("Escape");
    assert.equal(await panel.isVisible(), width !== 390);
    await link.click();
    const dialog = page.getByRole("dialog", { name: "Media viewer", exact: true });
    await dialog.getByRole("region", { name: target, exact: true }).waitFor();
    if (width === 390) await dialog.getByRole("button", { name: "More options", exact: true }).click();
    await dialog.getByRole("button", { name: "Show in folder", exact: true }).filter({ visible: true }).click();
    await dialog.waitFor({ state: "detached" });
    // Do not use locator.click/scrollIntoViewIfNeeded here: those would repair the broken scroll for the app.
    await page.waitForFunction(target => {
      const row = document.querySelector(`button[data-shared-row="${target}"]`);
      const viewport = document.querySelector(".files-tree .virtual");
      if (!row || !viewport) return false;
      const bounds = row.getBoundingClientRect(), view = viewport.getBoundingClientRect();
      return view.height > 0 && bounds.top >= view.top && bounds.bottom <= view.bottom &&
        row.getAttribute("aria-pressed") === "true";
    }, target, { timeout: 5000 });
    assert.ok(await scroll.evaluate(node => node.scrollTop > 0), "Show in folder scrolls to the offscreen target");
    assert.equal(await panel.getByRole("button", { name: "Files", exact: true }).getAttribute("aria-pressed"), "true");
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: `${output}/show-in-folder-${width}.png` });
    assert.deepEqual(errors, []);
    await context.close();
  }
  console.log("PASS: Show in folder reveals and selects an offscreen file at 390 and 1440 px.");
} finally {
  await browser.close();
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
