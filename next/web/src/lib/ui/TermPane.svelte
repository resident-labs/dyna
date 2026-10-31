<script lang="ts" module>
  import type { GhosttyTheme } from "../ghostty/core.ts";
  // ANSI colors per theme, in order: black, red, green, yellow, blue, magenta, cyan, white, then the
  // bright set. The terminal follows the app's theme, as T3 Code's does; v1's was dark only.
  const palettes = {
    dark: ["#2b2a2d", "#ef8a80", "#9ccf9a", "#e8c27a", "#8fa8f0", "#d3a6ef", "#8cc8e3", "#d8d3cb",
      "#6c675f", "#f4a59c", "#b4dcb2", "#f0d39c", "#bccbff", "#e6c6f7", "#b0d9ee", "#f3efe9"],
    light: ["#2a2723", "#b3332e", "#3d7a3a", "#8a6100", "#3d5bd0", "#8a3fae", "#1f7a8c", "#d8d3cb",
      "#63605a", "#c9463f", "#4c8f48", "#a37300", "#5571db", "#a050c2", "#2a8ea3", "#fbf9f6"],
  };
  const rgb = (hex: string) => { const n = parseInt(hex.trim().slice(1), 16); return { r: n >> 16 & 255, g: n >> 8 & 255, b: n & 255 }; };
  // The background, text and cursor come from the app's tokens, so the terminal matches the dock.
  function theme(dark: boolean): GhosttyTheme {
    const css = getComputedStyle(document.documentElement);
    const token = (name: string) => rgb(css.getPropertyValue(name));
    return { background: token("--bg"), foreground: token("--text"), cursor: token("--focus"),
      palette: palettes[dark ? "dark" : "light"].map(rgb), selectionBackground: dark ? "rgb(255 235 210 / 22%)" : "rgb(61 91 208 / 22%)" };
  }
  // Canvas font strings reject CSS keywords such as ui-monospace, so only the named faces go in; the
  // surface adds its own monospace fallbacks.
  const font = () => ({ size: 13, family: getComputedStyle(document.documentElement).getPropertyValue("--mono")
    .split(",").map(face => face.trim()).filter(face => face && !/^(ui-)?monospace$/.test(face)).join(", ") });
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import { MediaQuery } from "svelte/reactivity";
  import type { GhosttyTerminalSurface } from "../ghostty/surface.ts";
  import { openTerminal, type TerminalTarget } from "../terminal.ts";
  // One live terminal, drawn by libghostty-vt on a canvas (see ../ghostty, vendored from v1). The socket
  // opens at the size the first fit measures; leaving closes it, and the engine hangs up the shell.
  let { target, label, onexit }: { target: TerminalTarget; label: string; onexit: (code?: number) => void } = $props();
  let host: HTMLElement;
  let surface: GhosttyTerminalSurface | undefined;
  let failed = $state(false);
  const dark = new MediaQuery("prefers-color-scheme: dark");
  export function focus() { surface?.focus(); }
  $effect(() => { const isDark = dark.current; surface?.setTheme(theme(isDark)); });
  onMount(() => {
    let gone = false;
    const socket = openTerminal($state.snapshot(target), { output: bytes => surface?.write(bytes), exit: code => { if (!gone) onexit(code); } });
    // The WebAssembly runtime loads only when the first terminal opens.
    import("../ghostty/surface.ts").then(async ({ GhosttyTerminalSurface: Surface }) => {
      const made = await Surface.create(host, {
        theme: theme(dark.current), font: font(),
        onData: data => socket.send(data),
        // dispose() flushes a pending resize, so a pane that has gone opens no shell.
        onResize: (cols, rows) => { if (!gone) socket.resize(cols, rows); },
        // F6 moves between the app's areas, so the shell never gets it.
        onSelectionChange: () => {}, beforeKey: event => event.key !== "F6",
        onLinkActivate: text => { window.open(text, "_blank", "noopener"); },
        canActivateLink: text => /^https?:\/\//i.test(text),
      });
      if (gone) { made.dispose(); return; }
      surface = made;
      made.focus();
    }).catch(() => { failed = true; });
    return () => { gone = true; surface?.dispose(); surface = undefined; socket.close(); };
  });
</script>

<div class="pane" role="group" aria-label={`Terminal, ${label}`}>
  {#if failed}<p>The terminal could not start in this browser.</p>{/if}
  <div class="host" bind:this={host}></div>
</div>

<style>
  .pane { flex: 1; display: flex; flex-direction: column; min-height: 0; min-width: 0; padding: 4px 0 0 8px; background: var(--bg); }
  .host { position: relative; flex: 1; min-height: 0; min-width: 0; overflow: hidden; }
  .host :global(.ghostty-canvas) { display: block; width: 100%; height: 100%; cursor: text; }
  .host :global(.ghostty-scrollbar) { position: absolute; top: 4px; right: 1px; bottom: 4px; z-index: 1; width: 8px; touch-action: none; }
  .host :global(.ghostty-scrollbar-thumb) { position: absolute; left: 1px; right: 1px; top: 0; border-radius: 3px; background: var(--line); }
  p { margin: 8px 0; color: var(--muted); font-size: 13px; }
</style>
