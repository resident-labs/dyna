<script lang="ts" module>
  // The window's sides that each resize handle moves: [left, top, right, bottom].
  const handles: [string, Edges][] = [
    ["n", [0, 1, 0, 0]], ["s", [0, 0, 0, 1]], ["e", [0, 0, 1, 0]], ["w", [1, 0, 0, 0]],
    ["ne", [0, 1, 1, 0]], ["nw", [1, 1, 0, 0]], ["se", [0, 0, 1, 1]], ["sw", [1, 0, 0, 1]],
  ];
</script>

<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import { clamp, dragged, type Edges, type Rect } from "../float.ts";
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
  const fit = (next: Rect) => clamp(next, window.innerWidth, window.innerHeight);
  // One pointer gesture on the title bar or a handle; the pointer capture ends it. Moving and
  // resizing change only the style, so the stream stays open.
  function grab(event: PointerEvent & { currentTarget: HTMLElement }, edges: Edges) {
    if (docked || event.button !== 0 || event.target instanceof Element && event.target.closest("button")) return;
    const start = rect, target = event.currentTarget, x = event.clientX, y = event.clientY;
    const move = (next: PointerEvent) => { rect = fit(dragged(start, edges, next.clientX - x, next.clientY - y)); };
    target.setPointerCapture(event.pointerId);
    target.addEventListener("pointermove", move);
    target.addEventListener("lostpointercapture", () => target.removeEventListener("pointermove", move), { once: true });
  }
  // The view takes focus when it opens; a window that no longer fits the viewport is brought inside.
  onMount(() => { if (!docked) rect = fit(rect); element?.focus(); });
  // Escape closes; Alt with an arrow key moves the floating window by 24 px.
  function key(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); onclose(); return; }
    const step = { ArrowLeft: [-24, 0], ArrowRight: [24, 0], ArrowUp: [0, -24], ArrowDown: [0, 24] }[event.key];
    if (docked || !event.altKey || !step) return;
    event.preventDefault();
    rect = fit({ ...rect, x: rect.x + step[0], y: rect.y + step[1] });
  }
</script>

<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; if (visible) failed = false; }} />
<svelte:window onresize={() => { if (!docked) rect = fit(rect); }} />
<section class="computer" class:floating={!docked} aria-label={`${name}'s computer`} tabindex="-1" bind:this={element} onkeydown={key}
  style:left={docked ? null : `${rect.x}px`} style:top={docked ? null : `${rect.y}px`}
  style:width={docked ? null : `${rect.w}px`} style:height={docked ? null : `${rect.h}px`}
>
  <!-- svelte-ignore a11y_no_static_element_interactions (dragging is for the pointer; Alt with an arrow key moves the window) -->
  <header class="label" onpointerdown={event => grab(event, [1, 1, 1, 1])}>{name}'s computer{#if !failed}<span class="live">Live</span>{/if}
    <button type="button" aria-label={docked ? "Undock" : "Dock in the sessions panel"} title={docked ? "Undock" : "Dock"} onclick={ondock}>
      <Icon name={docked ? "undock" : "dock"} /></button>
    <button type="button" aria-label="Close" title="Close" onclick={onclose}><Icon name="close" /></button></header>
  <!-- The image streams only while the owner can see it: this view open, the page visible. -->
  <div class="screen">
    {#if failed}<p>The live view stopped. <button type="button" class="retry" onclick={() => { failed = false; element?.focus(); }}>Try again</button></p>
    {:else if visible}<img src={`/v1/computers/watch?session=${encodeURIComponent(session)}`} alt={`${name}'s screen, live`} {@attach watch} />{/if}
  </div>
  {#if !docked}{#each handles as [side, edges] (side)}
    <!-- svelte-ignore a11y_no_static_element_interactions (resizing is for the pointer) -->
    <div class="handle {side}" onpointerdown={event => grab(event, edges)}></div>{/each}{/if}
</section>

<style>
  .computer { display: flex; flex-direction: column; min-height: 0; }
  .computer:focus { outline: none; }
  .computer:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .floating { position: fixed; z-index: 3; background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 12px 32px rgb(0 0 0 / 28%); }
  header { gap: 6px; padding: 0 6px 0 12px; }
  .floating header { height: 40px; cursor: grab; user-select: none; touch-action: none; }
  .live { margin-left: 2px; margin-right: auto; font-weight: 400; }
  header button { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  header button:hover { background: var(--hover); color: var(--text); }
  header button:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* Docked, the image keeps its ratio in a 16:9 box; floating, it fills the window below the title bar. */
  .screen { position: relative; display: grid; place-items: center; aspect-ratio: 16 / 9; margin: 12px; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
  .floating .screen { flex: 1; aspect-ratio: auto; margin: 0 8px 8px; }
  img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: contain; }
  p { margin: 0 12px; color: var(--muted); font-size: 13px; text-align: center; }
  /* Invisible 6 px resize handles on the edges, 12 px in the corners. */
  .handle { position: absolute; touch-action: none; }
  .n, .s { left: 12px; right: 12px; height: 6px; cursor: ns-resize; }
  .e, .w { top: 12px; bottom: 12px; width: 6px; cursor: ew-resize; }
  .n { top: -3px; } .s { bottom: -3px; } .e { right: -3px; } .w { left: -3px; }
  .ne, .nw, .se, .sw { width: 12px; height: 12px; }
  .ne { top: -3px; right: -3px; cursor: nesw-resize; } .sw { bottom: -3px; left: -3px; cursor: nesw-resize; }
  .nw { top: -3px; left: -3px; cursor: nwse-resize; } .se { bottom: -3px; right: -3px; cursor: nwse-resize; }
</style>
