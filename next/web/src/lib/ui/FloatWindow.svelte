<script lang="ts" module>
  // The window's sides that each resize handle moves: [left, top, right, bottom].
  const handles: [string, Edges][] = [
    ["n", [0, 1, 0, 0]], ["s", [0, 0, 0, 1]], ["e", [0, 0, 1, 0]], ["w", [1, 0, 0, 0]],
    ["ne", [0, 1, 1, 0]], ["nw", [1, 1, 0, 0]], ["se", [0, 0, 1, 1]], ["sw", [1, 0, 0, 1]],
  ];
</script>

<script lang="ts">
  import { onMount, type Snippet } from "svelte";
  import { clamp, dragged, type Edges, type Rect } from "../float.ts";
  // A view that floats as a window over the app or docks in its place in the layout. Docking changes
  // only the style, so the content never remounts. The rect belongs to the caller, so the window keeps
  // its place and size for the page view.
  let { label, docked, rect = $bindable(), element = $bindable(), kind, header, children, onkeydown }: {
    label: string; docked: boolean; rect: Rect; element?: HTMLElement; kind: string;
    header: Snippet; children: Snippet; onkeydown?: (event: KeyboardEvent) => void;
  } = $props();
  const fit = (next: Rect) => clamp(next, window.innerWidth, window.innerHeight);
  // One pointer gesture on the title bar or a handle, followed on the window until the pointer
  // goes up, as in v1.
  function grab(event: PointerEvent, edges: Edges) {
    if (docked || event.button !== 0 || event.target instanceof Element && event.target.closest("button")) return;
    event.preventDefault();
    const start = rect, x = event.clientX, y = event.clientY;
    const move = (next: PointerEvent) => { rect = fit(dragged(start, edges, next.clientX - x, next.clientY - y)); };
    const end = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", end); window.removeEventListener("pointercancel", end); };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
    window.addEventListener("pointercancel", end);
  }
  // A window that no longer fits the viewport is brought inside.
  onMount(() => { if (!docked) rect = fit(rect); });
  // Alt with an arrow key moves the floating window by 24 px; other keys go to the caller.
  function key(event: KeyboardEvent) {
    const step = { ArrowLeft: [-24, 0], ArrowRight: [24, 0], ArrowUp: [0, -24], ArrowDown: [0, 24] }[event.key];
    if (docked || !event.altKey || !step) { onkeydown?.(event); return; }
    event.preventDefault();
    rect = fit({ ...rect, x: rect.x + step[0], y: rect.y + step[1] });
  }
</script>

<svelte:window onresize={() => { if (!docked) rect = fit(rect); }} />
<!-- svelte-ignore a11y_no_noninteractive_element_interactions (Alt with an arrow key acts on the whole window) -->
<section class="window {kind}" class:floating={!docked} aria-label={label} tabindex="-1" bind:this={element} onkeydown={key}
  style:left={docked ? null : `${rect.x}px`} style:top={docked ? null : `${rect.y}px`}
  style:width={docked ? null : `${rect.w}px`} style:height={docked ? null : `${rect.h}px`}
>
  <!-- svelte-ignore a11y_no_static_element_interactions (dragging is for the pointer; Alt with an arrow key moves the window) -->
  <header class="label" onpointerdown={event => grab(event, [1, 1, 1, 1])}>{@render header()}</header>
  {@render children()}
  {#if !docked}{#each handles as [side, edges] (side)}
    <!-- svelte-ignore a11y_no_static_element_interactions (resizing is for the pointer) -->
    <div class="handle {side}" onpointerdown={event => grab(event, edges)}></div>{/each}{/if}
</section>

<style>
  .window { position: relative; display: flex; flex-direction: column; min-height: 0; }
  .window:focus { outline: none; }
  .window:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .floating { position: fixed; z-index: 3; background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 12px 32px rgb(0 0 0 / 28%); }
  header { gap: 6px; padding: 0 6px 0 12px; }
  .floating header { height: 40px; cursor: grab; user-select: none; touch-action: none; }
  header :global(button) { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  header :global(button:hover) { background: var(--hover); color: var(--text); }
  header :global(button:focus-visible) { outline: 2px solid var(--focus); outline-offset: 0; }
  /* Invisible 6 px resize handles on the edges, 12 px in the corners. */
  .handle { position: absolute; touch-action: none; }
  .n, .s { left: 12px; right: 12px; height: 6px; cursor: ns-resize; }
  .e, .w { top: 12px; bottom: 12px; width: 6px; cursor: ew-resize; }
  .n { top: -3px; } .s { bottom: -3px; } .e { right: -3px; } .w { left: -3px; }
  .ne, .nw, .se, .sw { width: 12px; height: 12px; }
  .ne { top: -3px; right: -3px; cursor: nesw-resize; } .sw { bottom: -3px; left: -3px; cursor: nesw-resize; }
  .nw { top: -3px; left: -3px; cursor: nwse-resize; } .se { bottom: -3px; right: -3px; cursor: nwse-resize; }
</style>
