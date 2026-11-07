// Build first, then: node test/input-batching.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Exercises the real built App with deterministic API fixtures, not a hand-built control mock.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/design/built/input-batching";
await mkdir(output, { recursive: true });
const dist = fileURLToPath(new URL("../dist/", import.meta.url));
const at = "2026-11-06T10:00:00Z";
const owner = { kind: "owner" };
const principal = session => ({ kind: "session", session });
function sessions(room) {
  return [["root", ""], ["build-lead", "root"], ["review-lead", "root"], ["builder", "build-lead"], ["deep-builder", "builder"]].map(([name, parent]) => ({
    id: `${room}-${name}`, name, parent: parent && `${room}-${parent}`, profile: "sol", box: `box-${room}-${name}`, boxState: "ready", computer: "stopped", goal: "", lastRead: 100, hadJobs: false, startedAt: at,
  }));
}
function events(room) {
  const p = name => principal(`${room}-${name}`);
  const posts = [
    [owner, [p("root"), p("build-lead")], "Please keep the room feed easy to follow."],
    [p("root"), [p("root"), p("build-lead")], "Keep going with the browser checks."],
    [p("build-lead"), [p("review-lead")], "The change is ready for independent review."],
    [p("builder"), [p("build-lead")], "The focused tests are passing."],
    [p("deep-builder"), [p("builder")], "I checked the remaining edge cases."],
    [p("deep-builder"), [owner], "The screenshot and persistence checks are ready."],
    [principal("closed-session"), [p("deep-builder")], "Historical update from a closed session."],
    [principal("room-b-root"), [p("build-lead")], "Update from the other room's root."],
  ].map(([from, mentions, text], i) => ({ seq: i + 1, id: `${room}-post-${i + 1}`, at, room, type: "post.created", from, body: { home: { kind: "feed" }, text, mentions } }));
  return [...posts.slice(0, 2), { seq: 9, id: `${room}-start`, at, room, type: "turn.started", from: { kind: "engine" }, body: { session: `${room}-deep-builder`, turn: "turn", inputs: [] } },
    { seq: 10, id: `${room}-end`, at: "2026-11-06T10:06:00Z", room, type: "turn.ended", from: { kind: "engine" }, body: { session: `${room}-deep-builder`, turn: "turn", outcome: "stopped", usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } }];
}
const rooms = ["room-a", "room-b"].map((id, i) => ({ id, name: i ? "horizon" : "dyna", paused: false, created: at, lastRead: 100, waiting: [], questions: [], working: [], failed: 0, hostless: 0 }));
let delivered = false;
function answer(method, request) {
 if (method === "events.list" && request.receiptSeqs) return { receipts: Object.fromEntries(request.receiptSeqs.map(seq => [seq, seq > 2 ? [] : [{ session: "room-a-root", name: "root", state: delivered ? "taken" : "pending", ...(!delivered && { batchDeadline: "2026-11-07T12:01:00Z" }) }, { session: "room-a-build-lead", name: "build-lead", state: "read" }]])) };
  switch (method) {
    case "room.list": return { rooms, seq: 10 };
    case "device.scopes": return { scopes: ["read", "operate", "admin"] };
    case "session.list": return { sessions: sessions(request.room ?? "room-a"), seq: 10 };
    case "events.list": return { events: events(request.room).filter(event => request.beforeSeq ? event.seq < request.beforeSeq : event.seq > request.afterSeq), names: { "closed-session": "closed-builder", "room-b-root": "horizon:root" } };
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
    response.write('event: hello\ndata: {"build":"input-batching-fixture"}\n\n');
    return;
  }
  if (url.pathname.startsWith("/v1/")) {
    let data = "";
    for await (const chunk of request) data += chunk;
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify(answer(url.pathname.slice(4), JSON.parse(data || "{}"))));
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
 const context = await browser.newContext();
 const page = await context.newPage();
 await page.addInitScript(() => { Date.now = () => Date.parse('2026-11-07T12:00:20Z'); });
 const errors = []; page.on('pageerror', e => errors.push(String(e)));
 for (const theme of ['light', 'carbon']) for (const width of [1440, 390]) {
  delivered = false;
  await page.setViewportSize({ width, height: 900 });
  await page.goto(`${base}/r/room-a`);
  await page.evaluate(theme => localStorage.setItem('dyna.theme', theme), theme);
  await page.reload();
  await page.locator('.receipt .countdown').first().waitFor({ timeout: 5000 });
  assert.equal(await page.locator('.receipt .countdown').first().textContent(), '· in 40 s');
  assert.equal(await page.locator('.receipt').count(), 2, 'owner and agent receipts');
  await page.locator('.receipt').first().focus();
  assert.match(await page.locator('.receipt .tooltip').first().textContent(), /root: Queued.*40 s[\s\S]*build-lead: Delivered/);
  await page.locator('main').click({ position: { x: 5, y: 5 } });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: `${output}/queued-${width}-${theme === 'carbon' ? 'dark' : 'light'}.png` });
  delivered = true;
  await page.locator('.receipt.fresh').first().waitFor();
  assert.equal(await page.locator('.receipt.fresh > span:not(.tooltip)').first().textContent(), 'Delivered');
  assert.equal(await page.locator('.receipt svg.draw').count(), 2);
  assert.equal(await page.locator('.receipt svg path').first().evaluate(el => getComputedStyle(el).animationDuration), '0.2s');
  await page.screenshot({ path: `${output}/delivered-${width}-${theme === 'carbon' ? 'dark' : 'light'}.png` });
  await page.waitForTimeout(3400);
  assert.equal(await page.locator('.receipt.fresh').count(), 0, 'settles to quiet check');
  await page.reload(); await page.locator('.receipt svg').first().waitFor();
  assert.equal(await page.locator('.receipt svg.draw').count(), 0, 'reload does not replay');
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
 }
 await page.emulateMedia({ reducedMotion: 'reduce' });
 delivered = false; await page.reload(); await page.locator('.receipt .countdown').first().waitFor();
 delivered = true; await page.locator('.receipt.fresh').first().waitFor();
 assert.equal(await page.locator('.receipt svg path').first().evaluate(el => getComputedStyle(el).animationName), 'none');
 assert.deepEqual(errors, []);
 console.log('PASS: real App feed owner/agent, aggregation, countdown, 200 ms draw, quiet settling, reload, reduced motion; eight screenshots. Deterministic API fixtures, not live delivery proof.');
 await context.close();
} finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
