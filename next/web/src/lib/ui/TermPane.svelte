<script lang="ts" module>
  import type { GhosttyTheme } from "../ghostty/core.ts";
  import type { Theme } from "../themes.ts";
  const rgb = (hex: string) => { const n = parseInt(hex.slice(1), 16); return { r: n >> 16 & 255, g: n >> 8 & 255, b: n & 255 }; };
  // The terminal follows the app's theme, as T3 Code's does; v1's was dark only. Its background, text and cursor are
  // the app's tokens, so it matches the dock.
  const ghosttyTheme = ({ tokens, terminal }: Theme): GhosttyTheme => ({ background: rgb(tokens["--bg"]), foreground: rgb(tokens["--text"]),
    cursor: rgb(tokens["--focus"]), palette: terminal.ansi.map(rgb), selectionBackground: terminal.selection });
  // Canvas font strings reject CSS keywords such as ui-monospace, so only the named faces go in; the
  // surface adds its own monospace fallbacks.
  const font = () => ({ size: 13, family: getComputedStyle(document.documentElement).getPropertyValue("--mono")
    .split(",").map(face => face.trim()).filter(face => face && !/^(ui-)?monospace$/.test(face)).join(", ") });
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import type { GhosttyTerminalSurface } from "../ghostty/surface.ts";
  import { keyBytes, withCtrl } from "../strip.ts";
  import { openTerminal, type TerminalSocket, type TerminalTarget } from "../terminal.ts";
  import { theme } from "../theme.svelte.ts";
  // One live terminal, drawn by libghostty-vt on a canvas (see ../ghostty, vendored from v1). The socket
  // opens at the size the first fit measures; leaving closes it, and the engine hangs up the shell.
  let { target, label, onexit, ctrl = $bindable(false) }: { target: TerminalTarget; label: string; onexit: (code?: number) => void; ctrl?: boolean } = $props();
  let host: HTMLElement;
  let surface: GhosttyTerminalSurface | undefined;
  let failed = $state(false);
  let socket: TerminalSocket | undefined;
  export function focus() { surface?.focus(); }
  // The phone key row (v1's send and key): a held Ctrl turns the next letter into its control character, and arrows
  // follow the shell's cursor mode.
  function send(text: string) {
    socket?.send(ctrl ? withCtrl(text) : text);
    ctrl = false;
  }
  export function key(name: Parameters<typeof keyBytes>[0]) { send(keyBytes(name, surface?.isApplicationCursorKeys() ?? false)); surface?.focus(); }
  $effect(() => { const next = ghosttyTheme(theme()); surface?.setTheme(next); });
  onMount(() => {
    let gone = false;
    socket = openTerminal($state.snapshot(target), { output: bytes => surface?.write(bytes), exit: code => { if (!gone) onexit(code); } });
    // The WebAssembly runtime loads only when the first terminal opens.
    import("../ghostty/surface.ts").then(async ({ GhosttyTerminalSurface: Surface }) => {
      const made = await Surface.create(host, {
        theme: ghosttyTheme(theme()), font: font(),
        onData: data => send(data),
        // dispose() flushes a pending resize, so a pane that has gone opens no shell.
        onResize: (cols, rows) => { if (!gone) socket?.resize(cols, rows); },
        // F6 moves between the app's areas, so the shell never gets it.
        onSelectionChange: () => {}, beforeKey: event => event.key !== "F6",
        onLinkActivate: text => { window.open(text, "_blank", "noopener"); },
        canActivateLink: text => /^https?:\/\//i.test(text),
      });
      if (gone) { made.dispose(); return; }
      surface = made;
      made.focus();
    }).catch(() => { failed = true; });
    return () => { gone = true; surface?.dispose(); surface = undefined; socket?.close(); socket = undefined; };
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
