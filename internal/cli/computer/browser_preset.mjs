// Holds a viewport preset on every page of the shared browser, including tabs opened later.
// argv: playwright-module endpoint out-dir options-json. It speaks raw CDP, so it needs no Playwright.
// New tabs pause at start until the preset is set, so the page's own scripts already see it.
// The overrides last only while this process holds its connection; SIGTERM or browser exit clears them.
import { writeFile } from "node:fs/promises";

const [, endpoint, dir, options] = process.argv.slice(2);
const { viewport, deviceScaleFactor, isMobile, hasTouch, userAgent } = JSON.parse(options);

const ws = new WebSocket(endpoint);
const pending = new Map();
let nextId = 0;
const send = (method, params = {}, sessionId) =>
  new Promise((resolve, reject) => {
    const id = ++nextId;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params, sessionId }));
  });

async function emulate(sessionId) {
  await send("Emulation.setDeviceMetricsOverride", { ...viewport, deviceScaleFactor, mobile: isMobile, screenWidth: viewport.width, screenHeight: viewport.height }, sessionId);
  await send("Emulation.setTouchEmulationEnabled", { enabled: hasTouch, maxTouchPoints: hasTouch ? 5 : 1 }, sessionId);
  await send("Emulation.setEmitTouchEventsForMouse", { enabled: hasTouch, configuration: "mobile" }, sessionId);
  if (userAgent) await send("Emulation.setUserAgentOverride", { userAgent }, sessionId);
}

ws.onmessage = async ({ data }) => {
  const msg = JSON.parse(data);
  if (msg.id) {
    const p = pending.get(msg.id);
    pending.delete(msg.id);
    return msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
  }
  if (msg.method !== "Target.attachedToTarget") return;
  const { sessionId, targetInfo, waitingForDebugger } = msg.params;
  if (targetInfo.type === "page") await emulate(sessionId).catch(() => {});
  if (waitingForDebugger) await send("Runtime.runIfWaitingForDebugger", {}, sessionId).catch(() => {});
  // A tab opened with a URL starts loading before the user agent and touch settings reach it, so load it again.
  if (waitingForDebugger && targetInfo.type === "page" && targetInfo.url.startsWith("http")) await send("Page.reload", {}, sessionId).catch(() => {});
};
ws.onclose = () => process.exit(0);
await new Promise((resolve) => (ws.onopen = resolve));
await send("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: true, flatten: true });
process.on("SIGTERM", () => process.exit(0));
await writeFile(`${dir}/ready`, "");
