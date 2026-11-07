// Run Vite, then node test/actions.browser.mjs /path/to/playwright/index.mjs [output-directory].
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3] ?? "/box/shared/design/built/github-actions-writes";
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox"] });
const page = await browser.newPage({ viewport: { width: 1440, height: 1200 } });
const errors = []; page.on("pageerror", error => errors.push(String(error)));
await page.addInitScript(() => localStorage.setItem("dyna.theme", "carbon"));
const room = { id: "room_fixture", name: "horizon", repos: ["resident-labs/horizon"], githubWrites: { repos: ["*"], groups: ["issues", "pulls", "projects"] } };
const requests = [];
await page.route("**/v1/**", async route => {
 const method = route.request().url().split("/v1/")[1];
 let body = {};
 if (method === "room.list") body = { rooms: [room] };
 if (method === "room.set") { const request = route.request().postDataJSON(); requests.push(request); room.githubWrites = request.githubWrites; }
 await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
});
await page.goto("http://localhost:5173/test/approvals.html");
const checkbox = page.getByRole("switch", { name: "GitHub writes: Actions in horizon", exact: true });
await checkbox.waitFor();
assert.equal(await checkbox.isChecked(), false);
await checkbox.check();
await page.waitForFunction(() => document.querySelector('input[aria-label="GitHub writes: Actions in horizon"]').checked);
assert.deepEqual(requests[0], { room: "room_fixture", githubWrites: { repos: ["*"], groups: ["issues", "pulls", "projects", "actions"] } });
await checkbox.uncheck();
await page.waitForFunction(() => !document.querySelector('input[aria-label="GitHub writes: Actions in horizon"]').checked);
assert.deepEqual(requests[1].githubWrites.groups, ["issues", "pulls", "projects"]);
await page.evaluate(() => document.fonts.ready);
await page.screenshot({ path: `${out}/settings-actions-1440-dark.png`, fullPage: true });
assert.deepEqual(errors, []);
await browser.close();
console.log("PASS: Actions default off, save on/off preserves groups; dark 1440 Settings screenshot (fixtures only)");
