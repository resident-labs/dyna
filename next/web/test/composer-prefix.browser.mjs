// Build first, then: node test/feed-depth.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Exercises the real built App with deterministic API fixtures, not a hand-built control mock.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/design/built/composer-prefix";
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
const calls = []; let refuseSwitch = false;
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
    response.setHeader("Content-Type", "application/json");
    if (url.pathname === "/v1/session.set_profile" && refuseSwitch) { response.statusCode = 400; response.end(JSON.stringify({ code: "invalid", message: "refused" })); return; }
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
  const page = await browser.newPage();
  const errors = []; page.on("pageerror", error => errors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem("dyna.theme", "carbon"));
  const input = page.getByRole("combobox", { name: "Message", exact: true });
  for (const width of [1440, 390]) for (const dm of [false, true]) {
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(base + "/r/room-a" + (dm ? "/s/room-a-builder" : ""));
    await input.waitFor(); await input.fill("!");
    await page.getByRole("listbox", { name: "Profiles" }).waitFor();
    assert.equal(await input.getAttribute("aria-expanded"), "true");
    await page.getByText(dm ? "Switch this session to…" : "Start a new session with…", { exact: true }).waitFor({ timeout: 2000 });
    assert.equal(await page.getByRole("button", { name: "Send", exact: true }).isDisabled(), true);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: output + "/" + (dm ? "dm" : "room") + "-" + width + "-dark.png", fullPage: true });
    await input.press("ArrowDown"); await input.press("Tab");
    assert.equal(await input.inputValue(), "!terra ");
    await page.getByText(dm ? "Switch to terra" : "New terra session", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "Send", exact: true }).isEnabled(), dm);
    if (!dm) await page.getByText("Add what the new session should do", { exact: true }).waitFor();
    await input.fill("!terra Do the work");
    await page.getByText(dm ? "Switch to terra" : "New terra session", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "Send", exact: true }).isEnabled(), true);
    await input.fill("!"); await input.press("Escape");
    assert.equal(await input.getAttribute("aria-expanded"), "false");
    for (const plain of ["normal text", "!!sol", "! text", "/sol task"]) {
      await input.fill(plain);
      assert.equal(await page.getByText(dm ? "Switch this session to…" : "Start a new session with…", { exact: true }).count(), 0);
      assert.equal(await page.getByRole("button", { name: "Send", exact: true }).isEnabled(), true);
    }
  }
  await page.goto(base + "/r/room-a"); await input.waitFor();
  await input.fill("!missing task"); assert.equal(await page.getByRole("button", { name: "Send", exact: true }).isDisabled(), true);
  await page.getByText("No profile named missing", { exact: true }).waitFor();
  await input.fill("!sol"); await page.getByText("Add what the new session should do", { exact: true }).waitFor();
  await input.fill("!sol Fix the bug"); await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForURL("**/s/room-a-builder");
  const start = calls.find(call => call.method === "session.start").request;
  assert.deepEqual({ ...start, clientId: "id" }, { clientId: "id", room: "room-a", profile: "sol", brief: "Fix the bug", name: "fix-the-bug-2" });
  await input.fill("!sol hello"); await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').value === "");
  const switched = calls.findIndex(call => call.method === "session.set_profile");
  assert.deepEqual(calls[switched].request, { session: "room-a-builder", profile: "sol" });
  assert.equal(calls.slice(switched + 1).find(call => call.method === "post").request.text, "hello");
  const posts = () => calls.filter(call => call.method === "post").length;
  let before = posts(); await input.fill("!sol"); await input.press("Escape"); await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').value === ""); assert.equal(posts(), before);
  refuseSwitch = true; await input.fill("!sol must not send"); await page.getByRole("button", { name: "Send", exact: true }).click();
  await page.getByText("The profile did not change. Try again.", { exact: true }).waitFor(); assert.equal(posts(), before);
  assert.equal(await input.inputValue(), "!sol must not send");
  assert.deepEqual(errors, []);
  console.log("PASS: full App room start, DM switch then post, switch-only, refusal, autocomplete keys, validation and four screenshots");
} finally { await browser.close(); server.closeAllConnections(); server.close(); }
