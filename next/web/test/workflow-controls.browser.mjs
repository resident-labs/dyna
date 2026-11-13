// Run against Vite: node test/workflow-controls.browser.mjs /path/to/playwright/index.mjs OUTPUT
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3];
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
for (const theme of ["light", "carbon"]) for (const width of [1280, 390]) {
 const context = await browser.newContext({ viewport: { width, height: 900 } });
 const page = await context.newPage();
 const errors = [];
 page.on("pageerror", error => errors.push(String(error)));
 await page.addInitScript(theme => localStorage.setItem("dyna.theme", theme), theme);
 let paused = false, refuse = false, steered;
 const at = "2026-11-13T09:25:00Z";
 const steps = ["Review contract", "Review worker delivery"].map((name, i) => ({ key: `worker-${i}`, kind: "agent", name, phase: "Review", profile: "sol", state: "running", started: at, usage: {} }));
 await page.route("**/v1/**", route => {
  const method = new URL(route.request().url()).pathname.split("/").at(-1);
  const body = route.request().postDataJSON();
  let response = {};
  if (method === "workflow.status") response = { run: { run: "wf-controls", room: "r", session: "run-session", name: "Workflow review", description: "Two independent reviews of the workflow controls.", profiles: ["sol"], state: "running", paused, started: at, usage: {} }, steps };
  else if (method === "workflow.pause") paused = true;
  else if (method === "workflow.unpause") paused = false;
  else if (method === "workflow.steer") {
   steered = body;
   if (refuse) return route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "conflict", message: "That worker has ended. Running workers: Review contract (worker-0)." }) });
  } else if (method === "transcript.read") response = { session: "run-session", turns: [] };
  else if (method === "events.list") response = { events: [], names: {}, nextSeq: 0 };
  return route.fulfill({ contentType: "application/json", body: JSON.stringify(response) });
 });
 const name = `${theme}-${width}`;
 await page.goto("http://localhost:5173/test/workflow-controls.html");
 await page.getByRole("button", { name: "Pause", exact: true }).click();
 await page.getByRole("button", { name: "Unpause", exact: true }).waitFor();
 await page.screenshot({ path: `${out}/${name}-paused.png` });
 await page.getByRole("button", { name: "Unpause", exact: true }).click();
 await page.getByRole("button", { name: "Pause", exact: true }).waitFor();
 await page.screenshot({ path: `${out}/${name}-running.png` });
 await page.goto("http://localhost:5173/test/workflow-controls.html?step=worker-1");
 const input = page.getByLabel("Steer this worker");
 await input.fill("Focus on delivery after a worker finishes.");
 await page.getByRole("button", { name: "Send", exact: true }).click();
 await page.waitForFunction(() => document.querySelector("#worker-steer")?.value === "");
 assert.equal(steered.step, "worker-1");
 assert.equal(steered.text, "Focus on delivery after a worker finishes.");
 await page.screenshot({ path: `${out}/${name}-worker.png` });
 refuse = true;
 await input.fill("Check the last race before returning.");
 await page.getByRole("button", { name: "Send", exact: true }).click();
 await page.getByRole("alert").waitFor();
 assert.equal(await input.inputValue(), "Check the last race before returning.");
 await page.screenshot({ path: `${out}/${name}-error.png` });
 assert.deepEqual(errors, []);
 assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no horizontal page overflow");
 await context.close();
 console.log(`${name}: pause, unpause, worker target, retry text and renders passed`);
}
await browser.close();
