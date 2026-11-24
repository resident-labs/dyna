// Build first, then: node test/session-clear.browser.mjs /tmp/pw/node_modules/playwright-core/index.mjs [output-directory]
// Exercises the real built App with deterministic API fixtures and captures dark 1440 & 390 screenshots.
import assert from "node:assert/strict";
import http from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const { chromium } = await import(process.argv[2]);
const output = process.argv[3] ?? "/box/shared/design/built/fresh-start";
await mkdir(output, { recursive: true });
const dist = fileURLToPath(new URL("../dist/", import.meta.url));

const at1 = "2026-11-24T14:10:00Z";
const at2 = "2026-11-24T14:12:00Z";
const atClear = "2026-11-24T14:25:00Z";
const at3 = "2026-11-24T14:26:00Z";

const rooms = [{
  id: "room-dyna",
  name: "dyna",
  paused: false,
  created: at1,
  lastRead: 10,
  waiting: [],
  questions: [],
  working: [],
  failed: 0,
  hostless: 0,
}];

const sessions = [
  { id: "ses-root", name: "root", parent: "", profile: "opus", box: "box-root", boxState: "ready", computer: "stopped", goal: "", lastRead: 10, hadJobs: false, startedAt: at1 },
  { id: "ses-worker", name: "fresh-start", parent: "ses-root", profile: "sonnet", box: "box-worker", boxState: "ready", computer: "stopped", goal: "Keep files, goal and children intact across clear", lastRead: 10, hadJobs: false, startedAt: at1 },
];

let cleared = false;
const clearCalls = [];
const sseClients = new Set();

function pushEvent(event) {
  const payload = `data: ${JSON.stringify(event)}\n\n`;
  for (const res of sseClients) res.write(payload);
}

function transcriptFor(session) {
  if (session !== "ses-root") return { steps: [], truncated: false, turns: [], more: false };
  const turn1 = {
    id: "turn_1",
    started: at1,
    ended: at2,
    durationMs: 120000,
    outcome: "completed",
    more: false,
    inputs: [{
      id: "ev_1",
      seq: 1,
      room: "room-dyna",
      type: "post.created",
      at: at1,
      from: { kind: "owner" },
      home: { kind: "dm", session: "ses-root" },
      text: "Can you check the staging deploy status and remember ticket #412?",
    }],
    items: [{
      id: "item_1",
      turn: "turn_1",
      kind: "text",
      at: at2,
      text: "Checked staging: all three services are healthy, and I noted ticket #412 for follow-up.",
    }],
  };
  if (!cleared) return { steps: [], truncated: false, turns: [turn1], more: false };
  const turn2 = {
    id: "turn_2",
    started: at3,
    ended: "2026-11-24T14:26:18Z",
    durationMs: 18000,
    outcome: "completed",
    clearedAt: atClear,
    more: false,
    inputs: [{
      id: "ev_2",
      seq: 12,
      room: "room-dyna",
      type: "post.created",
      at: at3,
      from: { kind: "owner" },
      home: { kind: "dm", session: "ses-root" },
      text: "Let's review the release checklist for today.",
    }],
    items: [{
      id: "item_2",
      turn: "turn_2",
      kind: "text",
      at: "2026-11-24T14:26:18Z",
      text: "Starting fresh on the release checklist — I still have your workspace files and active goals.",
    }],
  };
  return { steps: [], truncated: false, turns: [turn2, turn1], more: false };
}

function answer(method, req) {
  switch (method) {
    case "room.list": return { rooms, seq: 10 };
    case "device.scopes": return { scopes: ["read", "operate", "admin"] };
    case "profile.list": return { profiles: ["opus", "sonnet", "neon"].map(name => ({ profile: { name, taste: 9, intelligence: 9, cost: 4, model: name, thinking: "high" }, inUse: 1 })) };
    case "session.list": return { sessions, seq: 10 };
    case "session.account": return { choices: [] };
    case "events.list": return { events: [], names: { "ses-root": "root", "ses-worker": "fresh-start" } };
    case "transcript.read": return transcriptFor(req.session);
    case "session.clear": {
      clearCalls.push(req);
      cleared = true;
      pushEvent({ seq: 11, id: "ev-clear", at: atClear, room: "room-dyna", type: "session.cleared", from: { kind: "owner" }, body: { session: req.session } });
      return {};
    }
    case "workflow.list": return { runs: [] };
    case "op.list": return { operations: [] };
    case "question.list": return { questions: [] };
    case "goal.list": return { goals: [] };
    case "pr.list": return { prs: [] };
    case "host.list": return { hosts: [] };
    case "accounts.list":
    case "accounts.quota": return { accounts: [] };
    default: return {};
  }
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, "http://fixture");
  if (url.pathname === "/v1/events") {
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.write('event: hello\ndata: {"build":"fresh-start-fixture"}\n\n');
    sseClients.add(response);
    request.on("close", () => sseClients.delete(response));
    return;
  }
  if (url.pathname.startsWith("/v1/")) {
    let data = "";
    for await (const chunk of request) data += chunk;
    response.setHeader("Content-Type", "application/json");
    response.end(JSON.stringify(answer(url.pathname.slice(4), JSON.parse(data || "{}"))));
    return;
  }
  const relative = url.pathname.slice(1);
  const file = relative.startsWith("assets/") ? path.join(dist, relative) : path.join(dist, "index.html");
  const mime = { ".js": "text/javascript", ".css": "text/css", ".html": "text/html", ".woff2": "font/woff2", ".woff": "font/woff" }[path.extname(file)] ?? "application/octet-stream";
  response.writeHead(200, { "Content-Type": mime });
  response.end(await readFile(file));
});

await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });

try {
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(String(error)));
  await page.addInitScript(() => localStorage.setItem("dyna.theme", "carbon"));

  for (const width of [1440, 390]) {
    cleared = false;
    await page.setViewportSize({ width, height: width === 1440 ? 900 : 844 });
    await page.goto(`${base}/r/room-dyna/s/ses-root`);
    await page.getByText("Checked staging: all three services are healthy", { exact: false }).waitFor({ timeout: 5000 });
    await page.evaluate(() => document.fonts.ready);

    // Open the DM header menu ("Session menu" on desktop, "Room menu" on phone) and verify "Clear conversation…" is present.
    const moreBtn = page.getByRole("button", { name: width === 1440 ? "Session menu" : "Room menu", exact: true });
    await moreBtn.waitFor();
    await moreBtn.click();
    const clearItem = page.getByRole("menuitem", { name: "Clear conversation…", exact: true });
    await clearItem.waitFor();
    await page.screenshot({ path: `${output}/menu-${width}-dark.png` });

    // Click "Clear conversation…" to open the confirmation dialog.
    await clearItem.click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    await page.getByText("Start root fresh?", { exact: true }).waitFor();
    await page.getByText("It forgets this conversation; its files, goal and children stay.", { exact: true }).waitFor();
    // Wait for modal enter transition (opacity + transform) to settle before taking the screenshot.
    await page.waitForFunction(() => {
      const el = document.querySelector("dialog[open]");
      return el && parseFloat(getComputedStyle(el).opacity) > 0.99;
    });
    await page.screenshot({ path: `${output}/confirm-${width}-dark.png` });

    // Confirm and verify session.clear is called and the "Conversation cleared" divider renders.
    await dialog.getByRole("button", { name: "Clear conversation", exact: true }).click();
    await page.getByRole("separator").filter({ hasText: "Conversation cleared" }).waitFor({ timeout: 5000 });
    await page.getByText("Starting fresh on the release checklist", { exact: false }).waitFor({ timeout: 5000 });
    await page.screenshot({ path: `${output}/divider-${width}-dark.png` });
  }

  assert.equal(clearCalls.length, 2);
  assert.deepEqual(clearCalls[0], { session: "ses-root" });
  assert.deepEqual(clearCalls[1], { session: "ses-root" });
  assert.deepEqual(errors, []);
  console.log(`PASS: captured menu, confirm dialog, and cleared divider at 1440 and 390 in ${output}`);
} finally {
  await browser.close();
  server.close();
}
