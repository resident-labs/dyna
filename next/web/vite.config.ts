import { readFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
// The build rewrites the entry script and drops its attributes. The engine's CSP allows no inline script, so this attribute
// is what keeps the first paint behind the module that applies the stored theme.
const renderBlocking: Plugin = { name: "render-blocking-entry", apply: "build", transformIndexHtml: { order: "post", handler: html => html.replace('<script type="module"', '<script type="module" blocking="render"') } };
// Dev only: trial-view.sh sets DYNA_TRIAL_COOKIE to a curl cookie jar, and the dev server sends /v1 (calls, the event
// stream and sockets) to the trial engine as its read-scope Web QA device. The engine refuses every write from that device.
const jar = process.env.DYNA_TRIAL_COOKIE;
const cookie = jar && readFileSync(jar, "utf8").split("\n").map(line => line.split("\t")).find(fields => fields[5] === "dyna_device")?.[6];
const trial = "dyna.localhost:7790";
export default defineConfig({
  plugins: [svelte(), renderBlocking],
  server: cookie ? { proxy: { "/v1": { target: "http://127.0.0.1:7790", ws: true,
    headers: { host: trial, origin: `http://${trial}`, cookie: `dyna_device=${cookie}` } } } } : undefined,
});
