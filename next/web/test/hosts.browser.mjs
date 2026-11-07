// Start Vite, then node test/hosts.browser.mjs /path/to/playwright/index.mjs [output-directory].
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3] ?? "/box/shared/design/built/ci-scale";
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const errors = []; page.on("pageerror", e => errors.push(String(e)));
await page.addInitScript(() => localStorage.setItem("dyna.theme", "carbon"));
const gib = 1024 ** 3;
const host = { name: "ovh-rise5", reachable: true, boxes: 5, cpus: 47, memBytes: 122*gib, cpuUsed: 7.5, memUsed: 13*gib, diskBytes: 900*gib, diskUsed: 120*gib, diskLow: false, agents: true, ci: true, ciScale: 1, runners: 8, shared: [], history: [] };
let refusal = false; const requests = [];
await page.route("**/v1/**", async route => {
 const method = route.request().url().split("/v1/")[1];
 let body = {};
 if (method === "host.list") body = { hosts: [host, { ...host, name: "arch", ci: false, ciScale: 1, runners: 0, cpus: 16, memBytes: 64*gib }] };
 if (method === "room.list") body = { rooms: [] };
 if (method === "host.set") {
  const req = route.request().postDataJSON(); requests.push(req);
  if (refusal) { await route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ code: "invalid", message: "Cannot save capacity" }) }); return; }
  if (req.ciScale !== undefined) host.ciScale = req.ciScale;
  if (req.ci !== undefined) host.ci = req.ci;
 }
 await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
});
await page.goto("http://localhost:5173/test/hosts.html");
const select = page.getByRole("combobox", { name: "CI capacity" });
await select.waitFor();
assert.equal(await select.inputValue(), "1");
assert.deepEqual(await select.locator("option").allTextContents(), ["1x", "2x", "3x", "4x"]);
await select.selectOption("2");
await page.waitForFunction(() => document.querySelector('select[aria-label="CI capacity"]').value === "2");
assert.deepEqual(requests[0], { host: "ovh-rise5", ciScale: 2 });
await page.evaluate(() => document.fonts.ready);
await page.screenshot({ path: `${out}/hosts-1440-dark.png`, fullPage: true });
refusal = true; await select.selectOption("3");
await page.waitForFunction(() => document.querySelector('select[aria-label="CI capacity"]').value === "2");
refusal = false;
await page.getByRole("switch", { name: "CI", exact: true }).uncheck();
await select.waitFor({ state: "detached" });
assert.equal(errors.length, 0, errors.join("\n"));
await browser.close();
console.log("PASS: options, default, save, refusal rollback, CI-off visibility; 1440 dark screenshot");
