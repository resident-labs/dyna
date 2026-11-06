// Run against a freshly started dyna-webdev -fixture empty and Vite at :5173.
// WEBDEV_LOG names its login URL log. SHARED_ROOT is this room's real /box/shared.
// The dev engine's temporary room gets a symlink to that folder; no file there is changed.
// node test/room-files.browser.mjs /path/to/playwright/index.mjs /box/shared/design/built/room-files
import assert from "node:assert/strict";
import { mkdir, readFile, readdir, stat, symlink } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3];
assert.ok(out && process.env.WEBDEV_LOG && process.env.SHARED_ROOT, "output, WEBDEV_LOG and SHARED_ROOT are required");
await mkdir(out, { recursive: true });
const browser = await chromium.launch({ executablePath: "/usr/bin/chromium", args: ["--no-sandbox", "--host-resolver-rules=MAP dyna.localhost 127.0.0.1"] });
const setup = await browser.newContext();
const page = await setup.newPage();
const login = (await readFile(process.env.WEBDEV_LOG, "utf8")).split("\n")[0];
const origin = new URL(login).origin;
await page.goto(login);
const roomLink = page.locator(".rail a[href^='/r/']").first();
await roomLink.waitFor();
const room = (await roomLink.getAttribute("href")).split("/")[2];
const dirs = await Promise.all((await readdir("/tmp")).filter(name => name.startsWith("dyna-webdev-")).map(async name => ({ name, time: (await stat(`/tmp/${name}`)).mtimeMs })));
dirs.sort((a, b) => b.time - a.time);
const target = `/tmp/${dirs[0].name}/rooms/${room}`;
await mkdir(target, { recursive: true });
await symlink(process.env.SHARED_ROOT, `${target}/shared`);
await page.evaluate(async room => {
 const response = await fetch("/v1/post", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ room, text: "The design note is in `/box/shared/design/room-files.md`. Open it here without a duplicate attachment.", home: { kind: "feed" } }) });
 if (!response.ok) throw new Error(await response.text());
}, room);
const state = await setup.storageState();
await setup.close();
const urls = {
 tree: "design",
 markdown: "design/room-files.md",
 go: "design/pig-spike/pigspike.go",
 image: "design/settings-390-pages.png",
};
const errors = [];
for (const theme of ["carbon", "light"]) for (const width of [1440, 390]) {
 const context = await browser.newContext({ storageState: state, viewport: { width, height: 1000 }, permissions: ["clipboard-read", "clipboard-write"] });
 const page = await context.newPage();
 page.on("pageerror", error => errors.push(String(error)));
 await page.addInitScript(theme => localStorage.setItem("dyna.theme", theme), theme);
 const prefix = `${theme === "carbon" ? "dark" : "light"}-${width}`;
 await page.goto(`${origin}/r/${room}`);
 await page.getByRole("link", { name: "Files", exact: true }).click();
 await page.getByRole("navigation", { name: "Room files", exact: true }).waitFor();
 assert.ok(page.url().endsWith("/files/"));
 // Open two levels in the actual folder without fetching the whole tree.
 const tree = page.getByRole("navigation", { name: "Room files", exact: true });
 await tree.getByRole("button", { name: "Expand design", exact: true }).click();
 await tree.getByRole("button", { name: "Expand pig-spike", exact: true }).click();
 await tree.getByRole("link", { name: /pigspike.go/ }).waitFor();
 await page.evaluate(() => document.fonts.ready);
 await page.screenshot({ path: `${out}/${prefix}-tree.png` });
 for (const kind of ["markdown", "go", "image"]) {
  // Client navigation preserves expansion and exercises the actual tree links.
  const name = urls[kind].split("/").at(-1);
  await tree.getByRole("link", { name: new RegExp(name.replaceAll(".", "\\.")) }).click();
  if (kind === "image") await page.locator(".preview img").evaluate(img => img.decode());
  else await page.getByRole("region", { name, exact: true }).waitFor();
  if (kind === "go") await page.locator(".preview .hljs-keyword").first().waitFor();
  assert.equal(await page.locator(".room-files").evaluate(node => node.getBoundingClientRect().height), 1000, "Files fills the page");
  assert.equal(await page.locator("body").evaluate(node => node.scrollWidth <= innerWidth), true, "no horizontal overflow");
  await page.getByRole("button", { name: "Copy path", exact: true }).click();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `/box/shared/${urls[kind]}`);
  await page.screenshot({ path: `${out}/${prefix}-${kind}.png` });
  if (kind === "go") {
   const before = page.url();
   await page.getByRole("button", { name: "Open in viewer", exact: true }).click();
   await page.getByRole("dialog").waitFor();
   await page.keyboard.press("Escape");
   await page.getByRole("dialog").waitFor({ state: "detached" });
   assert.equal(page.url(), before, "Escape closes the viewer, not the Files view");
  }
  if (width === 390) await page.getByRole("link", { name: "Back to folder", exact: true }).click();
 }
 await page.goto(`${origin}/r/${room}/files/missing-file.md`);
 await page.getByText("Not found.", { exact: false }).waitFor();
 await page.getByRole("link", { name: "Back to shared files", exact: true }).click();
 await page.goBack(); await page.getByText("Not found.", { exact: false }).waitFor();
 await page.goForward(); await tree.waitFor();
 await page.goto(`${origin}/r/${room}`);
 const pathLink = page.locator('a[data-shared-path="design/room-files.md"]').first();
 await pathLink.waitFor();
 await page.screenshot({ path: `${out}/${prefix}-message.png` });
 await pathLink.click();
 await page.getByRole("dialog", { name: "Media viewer" }).waitFor();
 await page.getByRole("region", { name: "room-files.md", exact: true }).waitFor();
 await page.waitForTimeout(250); // Let the existing viewer's opening animation settle.
 await page.screenshot({ path: `${out}/${prefix}-modal.png` });
 await page.getByRole("button", { name: "Close", exact: true }).click();
 await context.close();
}
// Missing shared paths use the same failure state, including frames and download-only types.
const missingContext = await browser.newContext({ storageState: state });
const missingPage = await missingContext.newPage();
await missingPage.goto(`${origin}/r/${room}`);
await missingPage.evaluate(async room => {
 await fetch("/v1/post", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ room, text: ["md", "png", "html", "bin"].map(extension => `/box/shared/design/missing-room-files.${extension}`).join(" "), home: { kind: "feed" } }) });
}, room);
for (const extension of ["md", "png", "html", "bin"]) {
 await missingPage.locator(`a[data-shared-path="design/missing-room-files.${extension}"]`).first().click();
 const dialog = missingPage.getByRole("dialog");
 await dialog.getByText("The file did not load.", { exact: false }).waitFor();
 assert.ok((await dialog.getByRole("link", { name: "Show in folder", exact: true }).last().getAttribute("href")).endsWith("/files/design"));
 await dialog.getByRole("button", { name: "Close", exact: true }).click();
}
await missingContext.close();
// Post-render matcher tests run in a real DOM, independent of the app's network state.
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
console.log("Room files browser checks passed; 24 screenshots saved to " + out);
