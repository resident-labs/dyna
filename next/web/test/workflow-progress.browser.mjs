// Fixture browser test against Vite. node test/workflow-progress.browser.mjs PLAYWRIGHT_MODULE OUTPUT
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3];
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
for (const width of [1440, 390]) {
 const page = await browser.newPage({ viewport: { width, height: 1000 }, reducedMotion: "reduce" });
 const errors = [];
 page.on("pageerror", error => errors.push(String(error)));
 const usage = { model: "fixture", inputTokens: 3000, outputTokens: 500, cacheReadTokens: 1000, cacheWriteTokens: 0 };
 let calls = 12, done = false, reads = 0;
 const steps = () => [
  { key: "review", kind: "agent", name: "Review workflow delivery", profile: "sol", phase: "Review", state: done ? "done" : "running", started: "2026-11-13T09:25:00Z", ended: done ? "2026-11-13T09:30:00Z" : undefined, transcript: "review.jsonl", toolCalls: calls, lastActivity: "2026-11-13T09:29:58Z", usage },
  { key: "finished", kind: "agent", name: "Check contract boundaries", profile: "sol", phase: "Review", state: "done", started: "2026-11-13T09:25:00Z", ended: "2026-11-13T09:28:00Z", transcript: "finished.jsonl", toolCalls: 8, usage },
  { key: "waiting", kind: "agent", name: "Check final results", profile: "sol", phase: "Review", state: "running", started: "2026-11-13T09:30:00Z", toolCalls: 0, usage: { ...usage, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0 } },
 ];
 await page.route("**/v1/**", route => {
  const method = new URL(route.request().url()).pathname.split("/").at(-1);
  assert.notEqual(method, "transcript.read", "progress does not fetch transcripts");
  let response = {};
  if (method === "workflow.status") {
   reads++;
   response = { run: { run: "wf-progress", room: "r", session: "run-session", name: "Workflow progress fixture", description: "Fixture data · Live worker activity and completed step counts.", profiles: ["sol"], state: done ? "done" : "running", failed: 0, started: "2026-11-13T09:25:00Z", usage }, steps: steps() };
  } else if (method === "events.list") response = { events: [], names: {}, nextSeq: 0 };
  return route.fulfill({ contentType: "application/json", body: JSON.stringify(response) });
 });
 await page.goto("http://localhost:5173/test/workflow-controls.html");
 await page.getByRole("heading", { name: "Workflow progress fixture" }).waitFor();
 await page.getByRole("button", { name: /^Review: .*Show its steps$/ }).click();
 const running = page.locator('[data-step="review"]');
 await running.getByText("12 tool calls", { exact: false }).waitFor();
 assert.match(await running.innerText(), /Last activity just now/);
 assert.equal(await running.locator(".activity").getAttribute("title"), "Last received transcript activity: 2026-11-13T09:29:58Z");
 assert.equal(await running.locator('[title^="Tokens so far:"]').innerText(), "4.5k");
 assert.match(await page.locator('[data-step="finished"]').innerText(), /8 tool calls/);
 assert.match(await page.locator(".step").filter({ hasText: "Check final results" }).innerText(), /0 tool calls.*Waiting for activity/s);
 await page.evaluate(() => document.fonts.ready);
 assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
 // Each progress row fits its box; hiding overflow on a parent cannot mask clipping.
 assert.ok(await page.locator(".progress").evaluateAll(nodes => nodes.every(node => node.scrollWidth <= node.clientWidth && node.getBoundingClientRect().right <= innerWidth)), "progress is not clipped");
 await page.screenshot({ path: `${out}/workflow-progress-fixture-${width}.png`, fullPage: true });
 calls = 13;
 await running.getByText("13 tool calls", { exact: false }).waitFor({ timeout: 12000 });
 assert.ok(reads >= 2, "live progress is polled");
 done = true;
 calls = 14;
 await running.getByText("14 tool calls", { exact: true }).waitFor({ timeout: 12000 });
 assert.equal(await running.locator(".activity").count(), 0, "finished row keeps final count without live activity wording");
 assert.equal(await running.locator('[title^="Tokens:"]').innerText(), "4.5k");
 assert.deepEqual(errors, []);
 console.log(`${width}: running, zero and finished tool counts, usage, activity, polling, and clipping checks passed`);
 await page.close();
}
await browser.close();
