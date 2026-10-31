import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
// The build rewrites the entry script and drops its attributes. The engine's CSP allows no inline script, so this attribute
// is what keeps the first paint behind the module that applies the stored theme.
const renderBlocking: Plugin = { name: "render-blocking-entry", apply: "build", transformIndexHtml: { order: "post", handler: html => html.replace('<script type="module"', '<script type="module" blocking="render"') } };
export default defineConfig({ plugins: [svelte(), renderBlocking] });
