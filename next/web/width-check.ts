import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import playwright, { type Page } from "playwright-core";
const { chromium } = playwright;
import { roomRoute, settingsPanes, settingsRoute } from "./src/lib/routes.ts";
import { knownWidthFailures, matchKnownFailure, staleWidthFailures } from "./known-width-failures.ts";

async function ready(page: Page) {
  await page.locator('[data-app-ready="true"]').waitFor();
  await page.waitForFunction(() => !document.querySelector('[data-page-ready="false"]'));
}

const widths = [1280, 920, 768, 390];
const fixtures = ["full", "empty"] as const;
const binary = process.env.DYNA_WEBDEV_BIN;
if (!binary) throw new Error("Set DYNA_WEBDEV_BIN to the built dyna-webdev binary");
const browser = await chromium.launch({ executablePath: process.env.DYNA_CHROMIUM, args: ["--no-sandbox"] });
const servers = fixtures.map((fixture, index) => spawn(binary, ["-web.listen", `127.0.0.1:${20000 + process.pid % 20000 + index}`, "-fixture", fixture], { stdio: ["ignore", "pipe", "inherit"] }));
try {
  const logins = await Promise.all(servers.map(server => new Promise<string>((resolve, reject) => {
    const lines = createInterface({ input: server.stdout });
    lines.once("line", line => { lines.close(); resolve(line); });
    server.once("exit", code => reject(new Error(`webdev exited before login: ${code}`)));
    server.once("error", reject);
  })));
  const failures: string[] = [];
  let known = 0;
  const seen = new Set<number>();
  await Promise.all(fixtures.map(async (fixture, index) => {
    const context = await browser.newContext();
    const login = await context.newPage();
    await login.goto(logins[index]);
    await ready(login);
    const origin = new URL(login.url()).origin;
    const rooms = await login.locator('nav[aria-label="Rooms"] a[href^="/r/"]').evaluateAll(links => links.map(link => link.getAttribute("href")).filter((href): href is string => href !== null));
    const routes = new Set(["/", ...rooms, "/settings", ...settingsPanes.map(pane => `/settings/${pane.id}`)]);
    for (const route of routes) {
      await login.goto(origin + route);
      await ready(login);
      for (const href of await login.locator('a[href^="/"]').evaluateAll(links => links.map(link => link.getAttribute("href")).filter((href): href is string => href !== null))) {
        if (roomRoute.test(href) || settingsRoute.test(href)) routes.add(href);
      }
    }
    await login.close();
    const tasks = [...routes].flatMap(route => widths.map(width => ({ route, width })));
    await Promise.all(Array.from({ length: 3 }, async () => {
      for (;;) {
        const task = tasks.pop();
        if (!task) break;
        const { route, width } = task;
        const page = await context.newPage();
        await page.setViewportSize({ width, height: 900 });
        try {
          // Chromium on hermeslab reported ERR_NETWORK_CHANGED during concurrent local navigations. Retry it once.
          await page.goto(origin + route, { waitUntil: "domcontentloaded" }).catch((error: unknown) => {
            if (error instanceof Error && error.message.includes("net::ERR_NETWORK_CHANGED")) return page.goto(origin + route, { waitUntil: "domcontentloaded" });
            throw error;
          });
          await ready(page);
          await page.evaluate(() => document.fonts.ready);
          const issues = await page.evaluate(() => {
            const found: { element: string; text: string }[] = [];
            if (document.documentElement.scrollWidth > document.documentElement.clientWidth) found.push({ element: "document", text: "" });
            for (const el of document.querySelectorAll<HTMLElement>("body *")) {
              const style = getComputedStyle(el);
              const text = el.textContent?.trim() ?? "";
              if (!text || !el.getClientRects().length || !["hidden", "clip"].includes(style.overflowX) || el.scrollWidth <= el.clientWidth) continue;
              const labelledBy = (el.getAttribute("aria-labelledby") ?? "").split(/\s+/).map(id => document.getElementById(id)?.textContent ?? "").join(" ").trim();
              const label = el.getAttribute("title") || el.getAttribute("aria-label") || labelledBy;
              if (style.textOverflow === "ellipsis" && label.includes(text)) continue;
              const element = `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}${typeof el.className === "string" ? el.className.split(/\s+/).filter(name => name && !name.startsWith("svelte-")).map(name => "." + name).join("") : ""}`;
              found.push({ element, text: text.slice(0, 100) });
            }
            return found;
          });
          const roomMatch = roomRoute.exec(route);
          const baselineRoute = roomMatch ? (roomMatch[2] ? "/r/:room/s/:session" : "/r/:room") : route.replace(/^\/settings\/accounts\/account\/[^/]+$/, "/settings/accounts/account/:account");
          const occurrences = new Map<number, number>();
          for (const issue of issues) {
            const line = `${fixture} ${route} ${width}px: ${issue.element}${issue.text ? `: ${issue.text}` : ""}`;
            const index = matchKnownFailure(knownWidthFailures, { fixture, route: baselineRoute, width, ...issue });
            const count = (occurrences.get(index) ?? 0) + 1;
            occurrences.set(index, count);
            if (index >= 0 && count <= (knownWidthFailures[index].occurrences ?? 1)) { known++; seen.add(index); console.warn(`Known: ${line}`); }
            else failures.push(line);
          }
        } catch (error: unknown) {
          const line = `${fixture} ${route} ${width}px: page did not finish: ${error instanceof Error ? error.message : String(error)}`;
          console.error(line);
          failures.push(line);
        } finally { await page.close(); }
      }
    }));
    await context.close();
  }));
  for (const stale of staleWidthFailures(knownWidthFailures, seen)) failures.push(`Remove fixed known width failure: ${stale.fixture} ${stale.route} ${stale.width}px: ${stale.element}: ${stale.text}`);
  if (failures.length) { console.error(failures.sort().join("\n")); process.exitCode = 1; }
  else console.log(`Width check passed with ${known} known failures.`);
} finally {
  await browser.close();
  for (const server of servers) server.kill("SIGTERM");
}
