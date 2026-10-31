<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import FloatWindow from "./FloatWindow.svelte";
  import type { Rect } from "../float.ts";
  // Floating, the view is a window over the app; docked, it fills the sessions panel. The rect
  // belongs to the app, so the window keeps its place and size for the page view.
  let { session, name, docked, rect = $bindable(), element = $bindable(), onclose, ondock }: {
    session: string; name: string; docked: boolean; rect: Rect; element?: HTMLElement; onclose: () => void; ondock: () => void;
  } = $props();
  let visible = $state(document.visibilityState === "visible");
  // A stopped computer closes this view, so an error here is a broken stream. The owner's retry
  // or the page becoming visible again tries once more; no timer does.
  let failed = $state(false);
  // Chromium may keep a multipart request open until a removed <img> is garbage-collected, so the
  // image drops its stream before it leaves. The error that the clearing fires is not a failure.
  function watch(image: HTMLImageElement) {
    let closing = false;
    image.addEventListener("error", () => { if (!closing) failed = true; });
    return () => { closing = true; image.src = "data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=="; };
  }
  // The view takes focus when it opens.
  onMount(() => { element?.focus(); });
  // Escape closes.
  function key(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); onclose(); }
  }
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible) failed = false; }} />
<FloatWindow label={`${name}'s computer`} kind="computer" {docked} bind:rect bind:element onkeydown={key}>
  {#snippet header()}{name}'s computer{#if !failed}<span class="live">Live</span>{/if}
    <button type="button" aria-label={docked ? "Undock" : "Dock in the sessions panel"} title={docked ? "Undock" : "Dock"} onclick={ondock}>
      <Icon name={docked ? "undock" : "dock"} /></button>
    <button type="button" aria-label="Close" title="Close" onclick={onclose}><Icon name="close" /></button>{/snippet}
  <!-- The image streams only while the owner can see it: this view open, the page visible. -->
  <div class="screen" class:docked>
    {#if failed}<p>The live view stopped. <button type="button" class="retry" onclick={() => { failed = false; element?.focus(); }}>Try again</button></p>
    {:else if visible}<img src={`/v1/computers/watch?session=${encodeURIComponent(session)}`} alt={`${name}'s screen, live`} {@attach watch} />{/if}
  </div>
</FloatWindow>

<style>
  .live { margin-left: 2px; margin-right: auto; font-weight: 400; }
  /* Docked, the image keeps its ratio in a 16:9 box; floating, the frame fills the window body edge to
     edge, and the window's own background fills the letterbox. */
  .screen { position: relative; display: grid; place-items: center; flex: 1; margin: 0; border-radius: 0 0 7px 7px; overflow: hidden; }
  .screen.docked { flex: 0 1 auto; aspect-ratio: 16 / 9; margin: 12px; border: 1px solid var(--line); border-radius: 8px; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  p { margin: 0 12px; color: var(--muted); font-size: 13px; text-align: center; }
</style>
