// Build first, then: node test/feed-depth.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Exercises the real built App with deterministic API fixtures, not a hand-built control mock.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/design/built/feed-depth";
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
function answer(method, request) {
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
    response.write('event: hello\ndata: {"build":"feed-depth-fixture"}\n\n');
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
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(String(error)));
  const select = page.getByRole("combobox", { name: "Feed visibility" });
  const count = async expected => {
    await page.waitForFunction(expected => document.querySelectorAll("article.post").length === expected, expected);
    assert.ok((await page.getByText("deep-builder worked for 6 min", { exact: false }).count()) > 0, "activity always visible");
  };
  await page.goto(`${base}/r/room-a`);
  await select.waitFor();
  assert.equal(await select.inputValue(), "all", "All is the default");
  assert.deepEqual(await select.locator("option").allTextContents(), ["Root", "Leads", "All"]);
  await count(8);
  await select.selectOption("root"); await count(3);
  assert.equal(await page.getByText("The change is ready for independent review.", { exact: true }).count(), 0, "lead to lead hidden in Root");
  assert.equal(await page.getByText("Historical update from a closed session.", { exact: true }).count(), 0, "unknown post is not a Root exemption");
  assert.equal(await page.evaluate(() => localStorage.getItem("dyna.feed-depth:room-a")), "root");
  await page.reload(); await select.waitFor(); assert.equal(await select.inputValue(), "root"); await count(3);
  await page.locator('nav[aria-label="Rooms"] a[href="/r/room-b"]').click();
  await page.waitForURL("**/r/room-b"); assert.equal(await select.inputValue(), "all", "new room defaults independently"); await count(8);
  await select.selectOption("leads"); await count(6);
  await page.reload(); await select.waitFor(); assert.equal(await select.inputValue(), "leads"); await count(6);
  await page.locator('nav[aria-label="Rooms"] a[href="/r/room-a"]').click();
  await page.waitForURL("**/r/room-a"); assert.equal(await select.inputValue(), "root"); await count(3);
  await select.focus(); await page.keyboard.press("Home"); await page.keyboard.press("ArrowDown"); await page.keyboard.press("Enter");
  assert.equal(await select.inputValue(), "leads", "native keyboard selection works"); await count(6);
  assert.equal(await page.getByText("The change is ready for independent review.", { exact: true }).count(), 1);
  assert.equal(await page.getByText("The focused tests are passing.", { exact: true }).count(), 0, "depth 2 builder to depth 1 build-lead hidden in Leads");
  assert.equal(await page.getByText("I checked the remaining edge cases.", { exact: true }).count(), 0, "known deep-to-worker hidden in Leads");
  assert.equal(await page.getByText("Historical update from a closed session.", { exact: true }).count(), 1, "unknown plus known deep fails open");
  assert.equal(await page.getByText("Keep going with the browser checks.", { exact: true }).count(), 1, "root to deep stays visible");
  assert.equal(await page.getByText("The screenshot and persistence checks are ready.", { exact: true }).count(), 1, "deep to owner stays visible");
  await page.evaluate(() => document.fonts.ready);
  // One representative screenshot is attached to the PR; the remaining sizes/themes are preserved as evidence.
  for (const theme of ["carbon", "light"]) for (const width of [1280, 390]) {
    await page.evaluate(theme => { localStorage.setItem("dyna.theme", theme); }, theme);
    await page.setViewportSize({ width, height: 1000 }); await page.reload(); await select.waitFor(); await count(6);
    await page.evaluate(() => document.fonts.ready);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no horizontal overflow");
    assert.ok(await page.locator("main > header .heading strong").evaluate(node => node.clientWidth >= node.scrollWidth), "short room title remains readable beside filter");
    await page.screenshot({ path: `${output}/feed-depth-${theme === "carbon" ? "dark" : theme}-${width}.png`, fullPage: true });
  }
  await page.evaluate(() => localStorage.setItem("dyna.feed-depth:room-a", "invalid"));
  await page.reload(); await select.waitFor(); assert.equal(await select.inputValue(), "all");
  assert.deepEqual(errors, []);
  console.log("PASS: real App default All, exact Root/Leads/unknown predicates, activity, room switch, reload persistence, keyboard and four theme/viewport screenshots.");
  await context.close();
} finally {
  await browser.close();
  server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
}
