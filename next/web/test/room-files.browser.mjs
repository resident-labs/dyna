// Full app + real room folder. Start a fresh dyna-webdev -fixture empty and Vite at :5173.
// WEBDEV_LOG names the dev engine's login URL log; SHARED_ROOT names this room's /box/shared.
// node test/room-files.browser.mjs /path/to/playwright/index.mjs /box/shared/design/built/room-files
import assert from "node:assert/strict";
import { mkdir, readFile, readdir, stat, symlink } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3];
assert.ok(out && process.env.WEBDEV_LOG && process.env.SHARED_ROOT);
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox", "--host-resolver-rules=MAP dyna.localhost 127.0.0.1"] });
const setup = await browser.newContext();
const start = await setup.newPage();
const login = (await readFile(process.env.WEBDEV_LOG, "utf8")).split("\n")[0];
const origin = new URL(login).origin;
await start.goto(login);
const roomLink = start.locator(".rail a[href^='/r/']").first();
await roomLink.waitFor();
const room = (await roomLink.getAttribute("href")).split("/")[2];
const dirs = await Promise.all((await readdir("/tmp")).filter(name => name.startsWith("dyna-webdev-")).map(async name => ({ name, time: (await stat(`/tmp/${name}`)).mtimeMs })));
dirs.sort((a, b) => b.time - a.time);
const target = `/tmp/${dirs[0].name}/rooms/${room}`;
await mkdir(target, { recursive: true });
await symlink(process.env.SHARED_ROOT, `${target}/shared`);
await start.evaluate(async room => {
 const response = await fetch("/v1/post", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ room, text: "The design note is in `/box/shared/design/room-files.md`. Open it here without a duplicate attachment.", home: { kind: "feed" } }) });
 if (!response.ok) throw new Error(await response.text());
}, room);
const state = await setup.storageState();
await setup.close();
const errors = [];
const md = "design/room-files.md", go = "design/pig-spike/pigspike.go";
const row = (page, path) => page.locator(`button[data-shared-row="${path}"]`);
async function find(page, path) {
 const scroll = page.locator(".files-tree .virtual");
 await scroll.waitFor();
 await scroll.evaluate(node => node.scrollTop = 0);
 for (let attempt = 0; attempt < 150; attempt++) {
  if (await row(page, path).count()) { await row(page, path).scrollIntoViewIfNeeded(); return row(page, path); }
  await scroll.evaluate(node => { node.scrollTop = node.scrollTop + node.clientHeight >= node.scrollHeight - 2 ? 0 : node.scrollTop + 240; });
  await page.waitForTimeout(30);
 }
 throw new Error("File row not found: " + path);
}
async function showFiles(page, width) {
 if (width === 390 && !await page.locator("aside.members").isVisible()) await page.locator("button.panel-toggle").click();
 await page.locator("aside.members").getByRole("button", { name: "Files", exact: true }).click();
 await page.getByRole("group", { name: "Room files", exact: true }).waitFor();
}
for (const theme of ["light", "carbon"]) for (const width of [1440, 390]) {
 const context = await browser.newContext({ storageState: state, viewport: { width, height: 1000 }, hasTouch: width === 390, permissions: ["clipboard-read", "clipboard-write"] });
 const page = await context.newPage();
 page.on("pageerror", error => errors.push(String(error)));
 await page.addInitScript(theme => localStorage.setItem("dyna.theme", theme), theme);
 const prefix = `${theme === "carbon" ? "dark" : "light"}-${width}`;
 await page.goto(`${origin}/r/${room}`);
 const pathLink = page.locator(`a[data-shared-path="${md}"]`).first();
 await pathLink.waitFor();
 assert.equal(await pathLink.evaluate(node => getComputedStyle(node).textDecorationLine), "none");
 assert.equal(await pathLink.evaluate(node => getComputedStyle(node).fontFamily === getComputedStyle(node.parentElement).fontFamily), true);
 await pathLink.hover();
 assert.equal(await pathLink.evaluate(node => getComputedStyle(node).textDecorationLine), "underline");
 await page.mouse.move(0, 0);
 if (width === 1440) await page.screenshot({ path: `${out}/${prefix}-message.png` });
 assert.equal(await page.getByRole("link", { name: "Files", exact: true }).count(), 0, "no Files header icon or route");
 await showFiles(page, width);
 assert.equal(new URL(page.url()).pathname, `/r/${room}`);
 assert.equal(await page.locator("aside.members").getByRole("button", { name: /^Sessions/ }).first().innerText(), "Sessions 1");
 assert.equal(await (await find(page, "design")).evaluate(node => node.getBoundingClientRect().height), 30, "compact file rows");
 await (await find(page, "design")).click();
 await (await find(page, "design/pig-spike")).click();
 await find(page, go);
 assert.equal(await page.locator(".files-tree .row").filter({ has: page.locator("button.fold") }).locator(".size").count(), 0, "no folder sizes");
 // Keep the expanded folder and its child folder together in the screenshot.
 await find(page, "design");
 await page.locator(".files-tree .virtual").evaluate(node => {
  const design = node.querySelector('[data-shared-row="design"]');
  if (design) node.scrollTop += design.getBoundingClientRect().top - node.getBoundingClientRect().top;
 });
 await page.evaluate(() => document.fonts.ready);
 await page.screenshot({ path: `${out}/${prefix}-files.png` });
 const examples = width === 1440 ? [["markdown", md], ["go", go]] : [["markdown", md]];
 for (const [kind, path] of examples) {
  const button = await find(page, path);
  await button.click();
  const dialog = page.getByRole("dialog", { name: "Media viewer", exact: true });
  await dialog.waitFor();
  await dialog.getByRole("region", { name: path.split("/").at(-1), exact: true }).waitFor();
  assert.equal(await dialog.locator("header .avatar").count(), 0, "shared files are not attributed to the owner");
  assert.equal(await dialog.getByRole("button", { name: "Jump to post", exact: true }).count(), 0, "tree files have no post jump");
  assert.ok((await dialog.getByRole("navigation", { name: "File path", exact: true }).innerText()).includes(path.split("/").at(-1)));
  await dialog.locator(".modified time").waitFor();
  assert.equal(await dialog.locator('button[title="Show in folder"]').first().getAttribute("title"), "Show in folder");
  const bytes = (await stat(`${process.env.SHARED_ROOT}/${path}`)).size;
  assert.ok((await dialog.locator("footer").innerText()).includes(bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`), "tree media carries its real size");
  if (kind === "go") {
   await dialog.locator(".hljs-keyword").first().waitFor();
   assert.equal(await dialog.locator("pre").evaluate(node => getComputedStyle(node).tabSize), "4");
   await dialog.getByRole("button", { name: "Previous media", exact: true }).click();
   await dialog.getByRole("region", { name: "pibase.sh", exact: true }).waitFor();
   await dialog.getByRole("button", { name: "Next media", exact: true }).click();
   await dialog.getByRole("region", { name: "pigspike.go", exact: true }).waitFor();
  }
  if (width === 390) assert.deepEqual(await dialog.evaluate(node => ({ width: node.getBoundingClientRect().width, height: node.getBoundingClientRect().height })), { width: 390, height: 1000 }, "phone viewer is full screen");
  await page.waitForTimeout(250);
  await page.screenshot({ path: `${out}/${prefix}-${kind}-modal.png` });
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "detached" });
  assert.equal(await page.locator("aside.members button[aria-pressed='true']").first().innerText(), "Files");
  assert.equal(await button.evaluate(node => document.activeElement === node), true, "closing returns focus to the file row");
 }
 // Existing row menu: right click, Copy path and Download. A long press uses the same menu.
 const file = await find(page, go);
 await file.click({ button: "right" });
 await page.getByRole("menuitem", { name: "Copy path", exact: true }).click();
 assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `/box/shared/${go}`);
 await file.click({ button: "right" });
 const download = page.waitForEvent("download");
 await page.getByRole("menuitem", { name: "Download", exact: true }).click();
 assert.equal((await download).suggestedFilename(), "pigspike.go");
 if (width === 390) {
  await file.dispatchEvent("pointerdown", { pointerType: "touch", clientX: 100, clientY: 600 });
  await page.waitForTimeout(500);
  await file.dispatchEvent("pointerup", { pointerType: "touch" });
  await page.getByRole("menuitem", { name: "Copy path", exact: true }).click();
  await page.waitForTimeout(750);
  await page.locator("aside.members").press("Escape");
 }
 // A path in the feed has no tree metadata: its Range response supplies the real footer size.
 await pathLink.click();
 const dialog = page.getByRole("dialog", { name: "Media viewer", exact: true });
 await dialog.getByRole("region", { name: "room-files.md", exact: true }).waitFor();
 await page.waitForFunction(() => document.querySelector(".media-viewer footer .count")?.textContent.includes("KB"));
 await dialog.locator(".modified time").waitFor();
 assert.equal(await dialog.locator("header .avatar").count(), 0);
 if (width === 390) await dialog.getByRole("button", { name: "More options", exact: true }).click();
 await dialog.getByRole("button", { name: "Show in folder", exact: true }).filter({ visible: true }).click();
 await dialog.waitFor({ state: "detached" });
 await row(page, md).waitFor();
 assert.equal(await row(page, md).getAttribute("aria-pressed"), "true");
 await page.waitForFunction(path => document.activeElement?.getAttribute("data-shared-row") === path, md);
 // Per-conversation tab memory survives in-app back and forward navigation.
 const panel = page.locator("aside.members");
 await panel.getByRole("button", { name: /^Sessions/ }).first().click();
 await panel.locator("a.member").first().click();
 if (width === 390) await page.locator("button.panel-toggle").click();
 await panel.getByRole("button", { name: "About", exact: true }).waitFor();
 assert.equal(await panel.getByRole("button", { name: "About", exact: true }).getAttribute("aria-pressed"), "true");
 await panel.getByRole("button", { name: "Files", exact: true }).click();
 await page.goBack();
 assert.equal(await panel.getByRole("button", { name: /^Sessions/ }).first().getAttribute("aria-pressed"), "true");
 await panel.getByRole("button", { name: "Files", exact: true }).click();
 await page.goForward();
 assert.equal(await panel.getByRole("button", { name: "Files", exact: true }).getAttribute("aria-pressed"), "true");
 await context.close();
}
// Missing-file recovery opens the parent in the panel, not an error document or a new route.
const missingContext = await browser.newContext({ storageState: state });
const missing = await missingContext.newPage();
await missing.goto(`${origin}/r/${room}`);
await missing.evaluate(async room => {
 await fetch("/v1/post", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ room, text: ["md", "png", "html", "bin"].map(extension => `/box/shared/design/missing-room-files.${extension}`).join(" "), home: { kind: "feed" } }) });
}, room);
for (const extension of ["md", "png", "html", "bin"]) {
 await missing.locator(`a[data-shared-path="design/missing-room-files.${extension}"]`).first().click();
 const dialog = missing.getByRole("dialog", { name: "Media viewer", exact: true });
 await dialog.getByText("The file did not load.", { exact: false }).waitFor();
 await dialog.locator(".state, .error, .visual-error").getByRole("button", { name: "Show in folder", exact: true }).click();
 await dialog.waitFor({ state: "detached" });
 assert.equal(await row(missing, "design").getAttribute("aria-pressed"), "true");
}
await missingContext.close();
// DOM-level path matcher tests: inline code, fences, links, punctuation and escaping.
const dom = await browser.newPage();
await dom.goto("http://localhost:5173/test/room-files-paths.html");
await dom.waitForFunction(() => typeof window.renderPaths === "function");
const result = await dom.evaluate(() => {
 const host = document.createElement("div");
 host.innerHTML = window.renderPaths('Read /box/shared/plain.md.,:; and `/box/shared/inline.go`.\n\n```text\n/box/shared/fenced.go\n```\n\n[existing /box/shared/link.md](https://example.com)\n\n/box/work/private /box/home/private\n\n"/box/shared/a&b.md"');
 return { paths: [...host.querySelectorAll("a[data-shared-path]")].map(a => a.dataset.sharedPath), code: host.querySelector("p code a")?.textContent, fenced: host.querySelector("pre")?.innerHTML, nested: host.querySelectorAll("a a").length, privateLinks: [...host.querySelectorAll("a")].filter(a => a.textContent.includes("/box/work") || a.textContent.includes("/box/home")).length };
});
assert.deepEqual(result.paths, ["plain.md", "inline.go", "a&b.md"]);
assert.equal(result.code, "/box/shared/inline.go");
assert.ok(!result.fenced.includes("<a"));
assert.equal(result.nested, 0);
assert.equal(result.privateLinks, 0);
assert.deepEqual(errors, []);
await browser.close();
console.log("Files panel browser checks passed; 12 screenshots saved to " + out);
