import { readFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
// The build rewrites the entry script and drops its attributes. The engine's CSP allows no inline script, so this attribute
// is what keeps the first paint behind the module that applies the stored theme.
const renderBlocking: Plugin = { name: "render-blocking-entry", apply: "build", transformIndexHtml: { order: "post", handler: html => html.replace('<script type="module"', '<script type="module" blocking="render"') } };
// Dev only: trial-view.sh sets DYNA_TRIAL_COOKIE to a curl cookie jar, and the dev server sends /v1 (calls and the event
// streams; the terminal's socket needs a scope this device lacks) to the trial engine as its read-scope Web QA device.
// The engine refuses every write from that device.
const jar = process.env.DYNA_TRIAL_COOKIE;
const cookie = jar && readFileSync(jar, "utf8").split("\n").map(line => line.split("\t")).find(fields => fields[5] === "dyna_device")?.[6];
const key = process.env.DYNA_TRIAL_KEY;
const trial = "dyna.localhost:7790";
// Only the browser that opened the run's one-time link reads the trial. The page keeps the key in sessionStorage, which no
// other origin (another localhost port included) can read, and sends it with each /v1 call in a header, or for an
// EventSource, which cannot send headers, in the query. A call without the key, or from another origin, is refused.
const sendKey = `const key = new URLSearchParams(location.search).get("key") ?? sessionStorage.getItem("trial-key") ?? "";
sessionStorage.setItem("trial-key", key);
const send = window.fetch;
window.fetch = (input, init = {}) => send(input, { ...init, headers: { ...Object.fromEntries(new Headers(init.headers)), "x-trial-key": key } });
window.EventSource = class extends EventSource { constructor(url, init) { const keyed = new URL(url, location.href); keyed.searchParams.set("trial_key", key); super(keyed, init); } };`;
const gate: Plugin = { name: "trial-gate", apply: "serve", transformIndexHtml: () => [{ tag: "script", attrs: { type: "module" }, children: sendKey, injectTo: "head-prepend" }],
  configureServer: server => {
    server.middlewares.use((req, res, next) => {
      if (!req.url?.startsWith("/v1/")) { next(); return; }
      const url = new URL(req.url, "http://view");
      const sent = req.headers["x-trial-key"] ?? url.searchParams.get("trial_key");
      url.searchParams.delete("trial_key");
      req.url = url.pathname + url.search;
      delete req.headers["x-trial-key"];
      if (sent === key && [undefined, `http://${req.headers.host}`].includes(req.headers.origin)) next();
      else res.writeHead(403).end("forbidden");
    });
  } };
export default defineConfig({
  plugins: [svelte(), renderBlocking, ...cookie && key ? [gate] : []],
  server: cookie && key ? { cors: false, proxy: { "/v1": { target: "http://127.0.0.1:7790",
    headers: { host: trial, origin: `http://${trial}`, cookie: `dyna_device=${cookie}` },
    configure: proxy => { proxy.on("proxyRes", answer => { delete answer.headers["set-cookie"]; }); } } } } : undefined,
});
