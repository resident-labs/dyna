import { readFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
// The build rewrites the entry script and drops its attributes. The engine's CSP allows no inline script, so this attribute
// is what keeps the first paint behind the module that applies the stored theme.
const renderBlocking: Plugin = { name: "render-blocking-entry", apply: "build", transformIndexHtml: { order: "post", handler: html => html.replace('<script type="module"', '<script type="module" blocking="render"') } };
// Dev only: trial-view.sh sets DYNA_TRIAL_COOKIE to a curl cookie jar, and the dev server sends /v1 (calls, the event
// streams and the WebSockets of the terminal and the computer view) to the trial engine as its read-scope Web QA device.
// The engine refuses every write from that device, and its sockets too. To try the terminal or a computer through the
// view, give the device those scopes: dyna device set-scopes <device> --scope read --scope terminal --scope computers.
const jar = process.env.DYNA_TRIAL_COOKIE;
const cookie = jar && readFileSync(jar, "utf8").split("\n").map(line => line.split("\t")).find(fields => fields[5] === "dyna_device")?.[6];
const key = process.env.DYNA_TRIAL_KEY;
const trial = "dyna.localhost:7790";
// Only the browser that opened the run's one-time link reads the trial. The page keeps the key in sessionStorage, which no
// other origin (another localhost port included) can read, and sends it with each /v1 call in a header, or for an
// EventSource or a WebSocket, which cannot send headers, in the query. A call without the key, or from another origin, is
// refused.
const sendKey = `const key = new URLSearchParams(location.search).get("key") ?? sessionStorage.getItem("trial-key") ?? "";
sessionStorage.setItem("trial-key", key);
const send = window.fetch;
const keyed = url => { const out = new URL(url, location.href); out.searchParams.set("trial_key", key); return out; };
window.fetch = (input, init = {}) => send(input, { ...init, headers: { ...Object.fromEntries(new Headers(init.headers)), "x-trial-key": key } });
window.EventSource = class extends EventSource { constructor(url, init) { super(keyed(url), init); } };
window.WebSocket = class extends WebSocket { constructor(url, protocols) { super(keyed(url), protocols); } };`;
// admit checks a /v1 request's key and origin, and takes the key off it before it goes to the engine.
function admit(req: { url?: string; headers: Record<string, string | string[] | undefined> }): boolean {
  const url = new URL(req.url ?? "/", "http://view");
  const sent = req.headers["x-trial-key"] ?? url.searchParams.get("trial_key");
  url.searchParams.delete("trial_key");
  req.url = url.pathname + url.search;
  delete req.headers["x-trial-key"];
  return sent === key && [undefined, `http://${req.headers.host}`].includes(req.headers.origin as string | undefined);
}
const gate: Plugin = { name: "trial-gate", apply: "serve", transformIndexHtml: () => [{ tag: "script", attrs: { type: "module" }, children: sendKey, injectTo: "head-prepend" }],
  configureServer: server => {
    server.middlewares.use((req, res, next) => {
      if (!req.url?.startsWith("/v1/")) { next(); return; }
      if (admit(req)) next();
      else res.writeHead(403).end("forbidden");
    });
  } };
// noVNC awaits at its top level as it loads, which needs ES2022; the computer view loads it on demand.
export default defineConfig({
  build: { target: "es2022" },
  optimizeDeps: { esbuildOptions: { target: "es2022" } },
  plugins: [svelte(), renderBlocking, ...cookie && key ? [gate] : []],
  // A WebSocket's upgrade skips the middlewares, so the proxy's bypass, which Vite calls for an upgrade without a
  // response, admits it, and a refused one is closed.
  server: cookie && key ? { cors: false, proxy: { "/v1": { target: "http://127.0.0.1:7790", ws: true,
    bypass: (req, res) => res || admit(req) ? undefined : false,
    headers: { host: trial, origin: `http://${trial}`, cookie: `dyna_device=${cookie}` },
    configure: proxy => { proxy.on("proxyRes", answer => { delete answer.headers["set-cookie"]; }); } } } } : undefined,
});
