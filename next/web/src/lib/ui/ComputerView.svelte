<script lang="ts" module>
  // Every image mount gets its own stream URL. Chromium can show a reused multipart URL from its image cache
  // without a new request: a frozen frame under "Live" after a retry, the page showing again, or the view reopening.
  let opened = 0;
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import FloatWindow from "./FloatWindow.svelte";
  import type { Rect } from "../float.ts";
  import { LiveStream, type Phase } from "../live.ts";
  // Floating, the view is a window over the app; docked, it sits above the session list in the sessions panel. The
  // rect belongs to the app, so the window keeps its place and size for the page view.
  let { session, name, root, activity, docked, full = false, rect = $bindable(), element = $bindable(), onclose, ondock }: {
    session: string; name: string; root: boolean; activity?: string; docked: boolean; full?: boolean; rect: Rect; element?: HTMLElement; onclose: (tapped: boolean) => void; ondock: (event: MouseEvent) => void;
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
    image.src = `/v1/computers/watch?session=${encodeURIComponent(session)}&n=${++opened}`;
    image.addEventListener("load", () => { frame(); if (image.naturalHeight > 1) ratio = image.naturalWidth / image.naturalHeight; });
    image.addEventListener("error", fail);
    return () => { stream.stop(); image.src = "data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw=="; };
  }
  // The view takes focus when it opens.
  onMount(() => { element?.focus(); });
  // Escape closes.
  function key(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); onclose(false); }
  }
  // Try again loads a new stream: the key remounts the image.
  let attempt = $state(0);
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible && phase === "failed") attempt++; }} />
<FloatWindow label={`${name}'s computer`} kind="computer" {docked} {full} bind:rect bind:element onkeydown={key}>
  {#snippet header()}<Avatar kind="session" {name} {root} size={20} /><span class="title">{name}'s computer</span>
    {#if phase === "live"}<span class="live">Live</span>{:else if phase === "connecting"}<span class="connecting">Connecting</span>{/if}
    {#if !full}<button type="button" class="first" aria-label={docked ? "Undock" : "Dock in the sessions panel"} title={docked ? "Undock" : "Dock"} onclick={ondock}>
      <Icon name={docked ? "undock" : "dock"} /></button>{/if}
    <button type="button" class:first={full} aria-label="Close" title="Close" onclick={event => onclose(event.detail > 0)}><Icon name="close" /></button>{/snippet}
  <!-- The image streams only while the owner can see it: this view open, the page visible. -->
  <div class="screen" class:docked class:full class:foot={activity} style:--ratio={ratio}>
    {#if visible}{#key attempt}<img alt={`${name}'s screen, live`} {@attach watch} />{/key}{/if}
    {#if phase === "failed"}<p class="badge" role="status">The live view stopped. <button type="button" class="retry" onclick={() => { attempt++; element?.focus(); }}>Try again</button></p>{/if}
  </div>
  <!-- The current step, one muted line under the screen: "qa-runner · reading the review state". -->
  {#if activity}<p class="activity" class:full>{name} · {activity}</p>{/if}
</FloatWindow>

<style>
  .title { flex: none; }
  /* A neutral outline label, the window's only uppercase text; it shows only once frames arrive. */
  .live { padding: 0 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 11px; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; }
  .connecting { color: var(--muted); font-weight: 400; }
  .first { margin-left: auto; }
  /* Full screen, the activity line and the band under the home bar stay near-black, as the screen is. */
  .activity.full { background: #0d0d0f; color: #d8d3cc; }
  :global(.computer.full)::after { content: ""; position: absolute; left: 0; right: 0; bottom: 0; height: env(safe-area-inset-bottom); background: #0d0d0f; }
  /* The screen is near-black in both themes, as a display is. Docked, it takes the panel width at the screen's ratio;
     floating, it fills the window body edge to edge and the dark background fills the letterbox. */
  .screen { position: relative; display: grid; place-items: center; flex: 1; margin: 0; border-radius: 0 0 7px 7px; overflow: hidden; background: #0d0d0f; }
  .screen.foot { border-radius: 0; }
  .screen.full { border-radius: 0; }
  .screen.docked { flex: none; aspect-ratio: var(--ratio); margin: 12px 12px 0; border: 1px solid var(--line); border-radius: 8px; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  /* v1's badge: a dark chip over the last picture, readable on any frame. */
  .badge { position: absolute; left: 12px; top: 12px; margin: 0; padding: 3px 11px; border-radius: 12px; background: rgb(20 19 22 / 82%); color: #d8d3cc; font-size: 12px; }
  .badge .retry { margin-left: 4px; color: #f4f0ea; }
  .activity { flex: none; margin: 0; padding: 6px 12px; overflow: hidden; color: var(--muted); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
</style>
