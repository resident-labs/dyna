// Build first, then: node test/composer-prefix-race.browser.mjs /path/to/playwright/index.mjs [evidence-directory]
// Exercises the real built App with deterministic API fixtures, not a hand-built control mock.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/tmp/composer-prefix-race";
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
const calls = []; let refuseSwitch = false; let pendingSwitch; let delaySwitch = false; let delayStart = false; let pendingStart;
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
    const result = answer(url.pathname.slice(4), JSON.parse(data || "{}"));
    if (url.pathname === "/v1/session.set_profile" && delaySwitch) { pendingSwitch = () => response.end(JSON.stringify(result)); return; }
    if (url.pathname === "/v1/session.start" && delayStart) { pendingStart = () => response.end(JSON.stringify(result)); return; }
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
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
try {
  const page = await browser.newPage();
  const errors = []; page.on("pageerror", error => errors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem("dyna.theme", "carbon"));
  const input = page.getByRole("combobox", { name: "Message", exact: true });

  await page.setViewportSize({width:1440,height:1000});
  const send = page.getByRole("button", {name:"Send",exact:true});
  const nav = async route => { await page.evaluate(route => { history.pushState({}, "", route); window.dispatchEvent(new PopStateEvent("popstate")); }, route); await input.waitFor(); };
  await page.goto(base + "/r/room-a/s/room-a-builder"); await input.waitFor();
  await input.fill("!sol old destination");
  delaySwitch = true; await send.click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').disabled);
  await nav("/r/room-b/s/room-b-root");
  console.log("Other DM busy:",await input.isDisabled());
  pendingSwitch();
  await page.waitForFunction(() => !document.querySelector('textarea[aria-label="Message"]').disabled);
  await page.waitForTimeout(100);
  assert.equal(calls.filter(c=>c.method==="post").at(-1).request.home.session,"room-a-builder");
  assert.equal(calls.filter(c=>c.method==="post").at(-1).request.room,"room-a");
  assert.equal(await input.inputValue(),"");
  await input.fill("other draft");
  await nav("/r/room-a/s/room-a-builder");
  assert.equal(await input.inputValue(),"");
  await nav("/r/room-b/s/room-b-root");
  assert.equal(await input.inputValue(),"other draft");
  console.log("PASS: delayed profile switch sends to captured DM and room, clears only old draft");
  await nav("/r/room-a/s/room-a-builder");
  await input.fill("!sol retry me"); await send.click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').disabled);
  await nav("/r/room-a/s/room-a-root"); await nav("/r/room-a/s/room-a-builder");
  pendingSwitch();
  await page.waitForFunction(() => !document.querySelector('textarea[aria-label="Message"]').disabled);
  await page.waitForTimeout(100);
  assert.equal(calls.filter(c=>c.method==="post").at(-1).request.text,"retry me");
  console.log("PASS: navigate away and back before success still posts to original DM");
  delaySwitch=false; refuseSwitch=true;
  const count=calls.filter(c=>c.method==="post").length;
  await input.fill("!sol refusal retained"); await send.click();
  await page.getByText("The profile did not change. Try again.",{exact:true}).waitFor();
  assert.equal(await input.inputValue(),"!sol refusal retained"); assert.equal(calls.filter(c=>c.method==="post").length,count);
  refuseSwitch=false; await send.click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').value === "");
  assert.equal(calls.filter(c=>c.method==="post").at(-1).request.text,"refusal retained");
  console.log("PASS: switch failure retains draft, enables retry and no early post");
  // Unmount the composer via the new-session page, then return while switch is pending.
  delaySwitch=true; await input.fill("!sol pending old text"); await send.click();
  await page.waitForFunction(() => document.querySelector('textarea[aria-label="Message"]').disabled);
  await page.evaluate(() => {history.pushState({},"","/r/room-a/new"); window.dispatchEvent(new PopStateEvent("popstate"));});
  await input.waitFor({state:"hidden"});
  await nav("/r/room-a/s/room-a-builder");
  assert.equal(await input.isDisabled(),false);
  await input.fill("NEW UNSENT DRAFT");
  await page.route("**/v1/files?*", route => route.fulfill({contentType:"application/json",body:JSON.stringify({name:"new-draft.txt",hash:"new-hash",mime:"text/plain",size:3})}));
  await page.locator('input[type="file"]').setInputFiles({name:"new-draft.txt",mimeType:"text/plain",buffer:Buffer.from("new")});
  await page.waitForTimeout(100);
  pendingSwitch(); await page.waitForTimeout(150);
  console.log("DRAFT AFTER PENDING SWITCH RESOLVES:",JSON.stringify(await input.inputValue()));
  console.log("OLD COMMAND POST:",JSON.stringify(calls.filter(c=>c.method==="post").at(-1).request));
  assert.equal(await input.inputValue(),"NEW UNSENT DRAFT", "Pending switch must not discard a newer draft after composer remount");

  assert.equal(calls.filter(c => c.method === "post").at(-1).request.attachments, undefined, "old command must not send the new file");
  await page.getByText("new-draft.txt", { exact: true }).waitFor();
  console.log("PASS: independent remount replay retains new text and new file; old command posts neither");

  await page.route("**/v1/files?*", route => {
    const name = new URL(route.request().url()).searchParams.get("name");
    return route.fulfill({ contentType: "application/json", body: JSON.stringify({ name, hash: name, mime: "text/plain", size: 3 }) });
  });
  const attach = async name => {
    await page.locator('input[type="file"]').setInputFiles({ name, mimeType: "text/plain", buffer: Buffer.from("abc") });
    await page.waitForFunction(() => !document.querySelector('button[aria-label="Send"]').disabled);
  };
  const remount = async route => {
    await page.evaluate(() => { history.pushState({}, "", "/r/room-a/new"); window.dispatchEvent(new PopStateEvent("popstate")); });
    await input.waitFor({ state: "hidden" }); await nav(route);
  };
  // A captured payload owns only the original file IDs and quote, never newly selected ones.
  await page.getByRole("button", { name: "Remove new-draft.txt", exact: true }).click();
  await input.fill("!sol captured text"); await attach("old-file.txt");
  pendingSwitch = undefined; await send.click();
  while (!pendingSwitch) await page.waitForTimeout(10);
  await remount("/r/room-a/s/room-a-builder");
  await input.fill("NEW TEXT WITH NEW QUOTE"); await attach("new-file.txt");
  pendingSwitch(); await page.waitForTimeout(150);
  const captured = calls.filter(c => c.method === "post").at(-1).request;
  assert.equal(captured.text, "captured text");
  assert.deepEqual(captured.attachments.map(file => file.name), ["old-file.txt"]);
  assert.equal(await input.inputValue(), "NEW TEXT WITH NEW QUOTE");
  await page.getByText("new-file.txt", { exact: true }).waitFor();
  assert.equal(await page.getByText("old-file.txt", { exact: true }).count(), 0);
  console.log("PASS: captured text/file posted; newer text/file retained across remount");

  // Switch-only consumes no files or quotes, and must not clear a newer text revision.
  await input.fill("!sol"); pendingSwitch = undefined; const beforeOnly = calls.filter(c => c.method === "post").length;
  await send.click(); while (!pendingSwitch) await page.waitForTimeout(10);
  await remount("/r/room-a/s/room-a-builder"); await input.fill("NEW SWITCH ONLY DRAFT"); await attach("switch-new.txt");
  pendingSwitch(); await page.waitForTimeout(100);
  assert.equal(calls.filter(c => c.method === "post").length, beforeOnly);
  assert.equal(await input.inputValue(), "NEW SWITCH ONLY DRAFT");
  await page.getByText("new-file.txt", { exact: true }).waitFor(); await page.getByText("switch-new.txt", { exact: true }).waitFor();
  console.log("PASS: switch-only keeps newer text and all draft files; no post");
  await input.fill("!sol"); pendingSwitch = undefined; await send.click();
  while (!pendingSwitch) await page.waitForTimeout(10);
  await remount("/r/room-a/s/room-a-builder");
  await input.fill("temporary revision"); await input.fill("!sol");
  pendingSwitch(); await page.waitForTimeout(100);
  assert.equal(await input.inputValue(), "!sol", "An edit back to the same text is still a newer revision");
  assert.equal(calls.filter(c => c.method === "post").length, beforeOnly);
  delaySwitch = false; refuseSwitch = true; await input.fill("!sol keep on failure"); await send.click();
  await page.getByText("The profile did not change. Try again.", { exact: true }).waitFor();
  assert.equal(await input.inputValue(), "!sol keep on failure");
  await page.getByText("new-file.txt", { exact: true }).waitFor(); await page.getByText("switch-new.txt", { exact: true }).waitFor();
  assert.equal(calls.filter(c => c.method === "post").length, beforeOnly);
  refuseSwitch = false;
  console.log("PASS: revision identity survives same-text edits; switch refusal keeps text/files and posts nothing");

  // Room start consumes only the submitted text revision, not source room files/quote or newer text.
  await nav("/r/room-a");
  const roomArticle = page.locator("article.post").filter({ hasText: "Please keep the room feed easy to follow." });
  await roomArticle.hover(); await roomArticle.getByRole("button", { name: "Forward", exact: true }).click();
  await input.fill("!sol Start a job"); await attach("room-old.txt");
  delayStart = true; await send.click(); while (!pendingStart) await page.waitForTimeout(10);
  await remount("/r/room-a"); await input.fill("NEW ROOM DRAFT"); await attach("room-new.txt");
  pendingStart(); await page.waitForURL("**/s/room-a-builder"); await nav("/r/room-a");
  assert.equal(await input.inputValue(), "NEW ROOM DRAFT");
  await page.getByText("room-old.txt", { exact: true }).waitFor(); await page.getByText("room-new.txt", { exact: true }).waitFor();
  await page.locator(".composer .quote").filter({ hasText: "Please keep the room feed easy to follow." }).waitFor();
  assert.equal(calls.filter(c => c.method === "post").length, beforeOnly);
  console.log("PASS: delayed room start retains new text and all source files/quote");
  // A replacement quote is another owner's newer draft data, not owned by the pending start.
  await input.fill("!sol Another job"); pendingStart = undefined; await send.click();
  while (!pendingStart) await page.waitForTimeout(10);
  await remount("/r/room-a"); await input.fill("NEWER ROOM TEXT");
  const replacement = page.locator("article.post").filter({ hasText: "Keep going with the browser checks." });
  await replacement.hover(); await replacement.getByRole("button", { name: "Forward", exact: true }).click();
  pendingStart(); await page.waitForURL("**/s/room-a-builder"); await nav("/r/room-a");
  assert.equal(await input.inputValue(), "NEWER ROOM TEXT");
  await page.locator(".composer .quote").filter({ hasText: "Keep going with the browser checks." }).waitFor();
  await page.getByText("room-old.txt", { exact: true }).waitFor(); await page.getByText("room-new.txt", { exact: true }).waitFor();
  console.log("PASS: delayed room start does not clear a replacement quote or newer text/files");
  delayStart = false; await input.fill("!sol Third job"); await send.click();
  await page.waitForURL("**/s/room-a-builder"); await nav("/r/room-a");
  assert.equal(await input.inputValue(), "", "An unchanged room command is cleared after success");
  await page.locator(".composer .quote").filter({ hasText: "Keep going with the browser checks." }).waitFor();
  await page.getByText("room-old.txt", { exact: true }).waitFor(); await page.getByText("room-new.txt", { exact: true }).waitFor();
  console.log("PASS: unchanged room command clears only text, retains files/quote");
  assert.deepEqual(errors, []);


} finally { await browser.close(); server.closeAllConnections(); server.close(); }
