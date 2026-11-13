// Build first, then: node test/voice.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Exercises the real built App with deterministic API fixtures, not a hand-built control mock.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/voice-browser";
await mkdir(output, { recursive: true });
const dist = fileURLToPath(new URL("../dist/", import.meta.url));
const at = "2026-11-06T10:00:00Z";
const owner = { kind: "owner" };
const principal = session => ({ kind: "session", session });
function sessions(room) {
  return [["root", ""], ["build-lead", "root"], ["review-lead", "root"], ["builder", "build-lead"], ["deep-builder", "builder"], ["fix-the-bug", "root"]].map(([name, parent]) => ({
    id: `${room}-${name}`, name, parent: parent && `${room}-${parent}`, profile: "sol", box: `box-${room}-${name}`, boxState: "ready", computer: "stopped", goal: "", lastRead: 100, hadJobs: false, startedAt: at,
  }));
}
function events(room) {
  const p = name => principal(`${room}-${name}`);
  const posts = [
    [owner, [p("root")], "Please keep the room feed easy to follow."],
    [p("root"), [p("deep-builder")], "Keep going with the browser checks."],
    [p("build-lead"), [p("review-lead")], "The change is ready for independent review."],
    [p("builder"), [p("build-lead")], "The focused tests are passing."],
    [p("deep-builder"), [p("builder")], "I checked the remaining edge cases."],
    [p("deep-builder"), [owner], "The screenshot and persistence checks are ready."],
    [principal("closed-session"), [p("deep-builder")], "Historical update from a closed session."],
    [principal("room-b-root"), [p("build-lead")], "Update from the other room's root."],
  ].map(([from, mentions, text], i) => ({ seq: i + 1, id: `${room}-post-${i + 1}`, at, room, type: "post.created", from, body: { home: { kind: "feed" }, text, mentions } }));
  return [...posts, { seq: 9, id: `${room}-start`, at, room, type: "turn.started", from: { kind: "engine" }, body: { session: `${room}-deep-builder`, turn: "turn", inputs: [] } },
    { seq: 10, id: `${room}-end`, at: "2026-11-06T10:06:00Z", room, type: "turn.ended", from: { kind: "engine" }, body: { session: `${room}-deep-builder`, turn: "turn", outcome: "stopped", usage: { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 } } }];
}
const rooms = ["room-a", "room-b"].map((id, i) => ({ id, name: i ? "horizon" : "dyna", paused: false, created: at, lastRead: 100, waiting: [], questions: [], working: [], failed: 0, hostless: 0 }));
const calls = [];
function answer(method, request) {
  calls.push({ method, request });
  switch (method) {
    case "profile.list": return { profiles: ["sol", "terra", "wonder-full"].map(name => ({ profile: { name, taste: 7, intelligence: 9, cost: 8, model: "gpt", thinking: "high" }, inUse: 1 })) };
    case "session.start": return { session: "room-a-builder" };
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
    case "accounts.list": case "accounts.quota": return { accounts: [{ id: "fixture-chatgpt", provider: "openai", state: "connected", rooms: null }] };
    default: return {};
  }
}
const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, "http://fixture");
  if (url.pathname === "/v1/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.write('event: hello\ndata: {"build":"feed-depth-fixture"}\n\n');
    return;
  }
  if (url.pathname.startsWith("/v1/")) {
    let data = "";
    for await (const chunk of request) data += chunk;
    response.setHeader("Content-Type", "application/json");
    const result = answer(url.pathname.slice(4), JSON.parse(data || "{}"));
    response.end(JSON.stringify(result));
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
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox", "--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"] });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = []; page.on('pageerror', error => errors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem('dyna.theme', 'carbon'));
  let socket;
  await page.routeWebSocket('**/v1/voice?*', ws => { socket = ws; ws.send(JSON.stringify({ type: 'ready' })); });
  await page.goto(base + '/r/room-a/s/room-a-root');
  await page.getByRole('combobox', { name: 'Message', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Call', exact: true }).click();
  await page.getByRole('status').filter({ hasText: 'Listening' }).waitFor();
  socket.send(JSON.stringify({ type: 'transcript', role: 'owner', text: 'Please check the latest fixture build.' }));
  socket.send(JSON.stringify({ type: 'response_done', working: true }));
  await page.getByRole('status').filter({ hasText: 'Working' }).waitFor();
  assert.equal(await page.locator('aside[aria-label="Voice call"] details').getAttribute('open'), null);
  await page.screenshot({ path: output + '/real-room-1440-dark.png' });
  assert.equal(await page.getByRole('button', { name: 'Close voice call' }).count(), 0);
  await page.getByRole('button', { name: 'Call', exact: true }).click();
  assert.equal(await page.locator('aside[aria-label="Voice call"]').evaluate(el => el === document.activeElement), true);
  const desktopComposer = await page.locator('.composer').boundingBox();
  const desktopWidget = await page.locator('aside[aria-label="Voice call"]').boundingBox();
  assert.ok(desktopWidget.x + desktopWidget.width <= desktopComposer.x + desktopComposer.width + 1);
  assert.ok(desktopWidget.y + desktopWidget.height <= desktopComposer.y);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(100);
  const composer = await page.getByRole('combobox', { name: 'Message', exact: true }).boundingBox();
  const widget = await page.locator('aside[aria-label="Voice call"]').boundingBox();
  assert.ok(widget.y + widget.height <= composer.y, 'phone widget stays above composer');
  const mute = await page.getByRole('button', { name: 'Mute', exact: true }).boundingBox();
  assert.ok(mute.height >= 44);
  await page.keyboard.press('Escape');
  assert.equal(await page.getByRole('button', { name: 'Hang up' }).count(), 1);
  await page.screenshot({ path: output + '/real-dm-390-dark.png' });
  await page.getByRole('button', { name: 'Mute', exact: true }).focus();
  await page.keyboard.press('Tab');
  assert.equal(await page.evaluate(() => document.activeElement.textContent), 'Hang up');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  assert.equal(await page.locator('.voice .dot').evaluate(el => getComputedStyle(el).animationName), 'none');
  await page.locator('.voice summary').click();
  for (let i = 0; i < 30; i++) socket.send(JSON.stringify({ type: 'transcript', role: 'owner', text: 'Fixture transcript line ' + i }));
  await page.waitForFunction(() => { const el = document.querySelector('.voice .transcript'); return el.scrollHeight > el.clientHeight && el.scrollHeight - el.clientHeight - el.scrollTop < 2; });
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('.voice details').getAttribute('open'), null);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.evaluate(() => { history.pushState({}, '', '/r/room-b/s/room-b-root'); window.dispatchEvent(new PopStateEvent('popstate')); });
  const crossroom = page.getByRole('button', { name: 'Call active in dyna', exact: true });
  assert.equal(await crossroom.textContent(), 'Call');
  await crossroom.click();
  await page.getByRole('button', { name: 'End it and call here' }).waitFor();
  await page.screenshot({ path: output + '/real-header-crossroom.png' });
  socket.send(JSON.stringify({ type: 'closed', reason: 'Fixture connection ended' }));
  await page.getByRole('button', { name: 'Call again' }).waitFor();
  await page.screenshot({ path: output + '/real-ended.png' });
  assert.equal(await page.getByRole('button', { name: 'Hang up' }).count(), 0);
  await page.getByRole('button', { name: 'Call again' }).click();
  await page.getByRole('status').filter({ hasText: 'Listening' }).waitFor();
  await page.getByRole('button', { name: 'Hang up' }).click();
  assert.deepEqual(errors, []);
  console.log('PASS: real App fixture room/DM, persistent call, cross-room offer, phone docking and touch targets, Escape, ended retry');
} finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
