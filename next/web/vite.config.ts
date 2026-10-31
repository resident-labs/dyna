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
// The view serves only the browser that opened its one-time link: that link sets a key cookie, and a request from another
// origin, such as another local page, is refused even with the cookie.
const gate: Plugin = { name: "trial-gate", apply: "serve", configureServer: server => {
  server.middlewares.use((req, res, next) => {
    if (new URL(req.url ?? "/", "http://view").searchParams.get("key") === key) {
      res.writeHead(303, { "set-cookie": `trial_key=${key}; HttpOnly; SameSite=Strict; Path=/`, location: "/" }).end();
    } else if (req.headers.cookie?.split("; ").includes(`trial_key=${key}`) && [undefined, `http://${req.headers.host}`].includes(req.headers.origin)) next();
    else res.writeHead(403).end("forbidden");
  });
} };
export default defineConfig({
  plugins: [svelte(), renderBlocking, ...cookie && key ? [gate] : []],
  server: cookie && key ? { cors: false, proxy: { "/v1": { target: "http://127.0.0.1:7790",
    headers: { host: trial, origin: `http://${trial}`, cookie: `dyna_device=${cookie}` },
    configure: proxy => { proxy.on("proxyRes", answer => { delete answer.headers["set-cookie"]; }); } } } } : undefined,
});
