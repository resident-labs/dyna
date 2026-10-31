<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import FloatWindow from "./FloatWindow.svelte";
  import type { Rect } from "../float.ts";
  import { LiveStream, type Phase } from "../live.ts";
  // Floating, the view is a window over the app; docked, it sits above the session list in the sessions panel. The
  // rect belongs to the app, so the window keeps its place and size for the page view.
  let { session, name, activity, docked, rect = $bindable(), element = $bindable(), onclose, ondock }: {
    session: string; name: string; activity?: string; docked: boolean; rect: Rect; element?: HTMLElement; onclose: () => void; ondock: () => void;
  } = $props();
  let visible = $state(document.visibilityState === "visible");
  // A stopped computer closes this view, so a failure here is a broken or silent stream. The owner's retry or the
  // page becoming visible again tries once more; nothing retries on a timer.
  let phase: Phase = $state("connecting");
  const stream = new LiveStream(next => { phase = next; });
  // The screen's shape, from the first frame; 16:9 until then.
  let ratio = $state(16 / 9);
  // Chromium may keep a multipart request open until a removed <img> is garbage-collected, so the
  // image drops its stream before it leaves. The load or error that the clearing fires belongs to a stopped stream,
  // so the view ignores it. A failed image stays, so the browser keeps showing the last frame under the notice, as in v1.
  function watch(image: HTMLImageElement) {
    const { frame, fail } = stream.start();
    image.addEventListener("load", () => { frame(); if (image.naturalHeight > 1) ratio = image.naturalWidth / image.naturalHeight; });
    image.addEventListener("error", fail);
    return () => { stream.stop(); image.src = "data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=="; };
  }
  // The view takes focus when it opens.
  onMount(() => { element?.focus(); });
  // Escape closes.
  function key(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); onclose(); }
  }
  // Try again loads a new stream: the key remounts the image, and the attempt in its URL keeps Chromium from showing
  // the old stream from its image cache without a new request.
  let attempt = $state(0);
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible && phase === "failed") attempt++; }} />
<FloatWindow label={`${name}'s computer`} kind="computer" {docked} bind:rect bind:element onkeydown={key}>
  {#snippet header()}<span class="title">{name}'s computer</span>{#if phase !== "failed"}<span class="live" class:connecting={phase === "connecting"}>{phase === "live" ? "Live" : "Connecting"}</span>{/if}
    {#if activity && !docked}<span class="caption">· {activity}</span>{/if}
    <button type="button" class="first" aria-label={docked ? "Undock" : "Dock in the sessions panel"} title={docked ? "Undock" : "Dock"} onclick={ondock}>
      <Icon name={docked ? "undock" : "dock"} /></button>
    <button type="button" aria-label="Close" title="Close" onclick={onclose}><Icon name="close" /></button>{/snippet}
  <!-- The image streams only while the owner can see it: this view open, the page visible. -->
  <div class="screen" class:docked style:--ratio={ratio}>
    {#if visible}{#key attempt}<img src={`/v1/computers/watch?session=${encodeURIComponent(session)}&attempt=${attempt}`} alt={`${name}'s screen, live`} {@attach watch} />{/key}{/if}
    {#if phase === "failed"}<p class="badge" role="status">The live view stopped. <button type="button" class="retry" onclick={() => { attempt++; element?.focus(); }}>Try again</button></p>{/if}
  </div>
  {#if docked && activity}<p class="activity">{activity}</p>{/if}
</FloatWindow>

<style>
  .title { flex: none; }
  .live { font-weight: 400; }
  .connecting { color: var(--muted); }
  .caption { min-width: 0; overflow: hidden; color: var(--muted); font-weight: 400; text-overflow: ellipsis; white-space: nowrap; }
  .first { margin-left: auto; }
  /* The screen is near-black in both themes, as a display is. Docked, it takes the panel width at the screen's ratio;
     floating, it fills the window body edge to edge and the dark background fills the letterbox. */
  .screen { position: relative; display: grid; place-items: center; flex: 1; margin: 0; border-radius: 0 0 7px 7px; overflow: hidden; background: #0d0d0f; }
  .screen.docked { flex: none; aspect-ratio: var(--ratio); margin: 12px 12px 0; border: 1px solid var(--line); border-radius: 8px; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  /* v1's badge: a dark chip over the last picture, readable on any frame. */
  .badge { position: absolute; left: 12px; top: 12px; margin: 0; padding: 3px 11px; border-radius: 12px; background: rgb(20 19 22 / 82%); color: #d8d3cc; font-size: 12px; }
  .badge .retry { margin-left: 4px; color: #f4f0ea; }
  .activity { margin: 8px 12px 4px; overflow: hidden; color: var(--muted); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
</style>
