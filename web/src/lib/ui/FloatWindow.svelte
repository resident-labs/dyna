<script lang="ts">
  // A floating window over an area: it moves by its title, resizes by its lower
  // right corner, and comes to the front on a press. The terminal pop-outs and
  // the Computer chat share it. The caller owns the rect and clamps it.
  import type { Snippet } from 'svelte'
  import type { Area, Rect } from '../sessions/termDock.ts'

  interface Props {
    rect: Rect
    area: Area
    label: string
    z?: number
    // data-term of the root, so a shortcut can find the window's shell.
    term?: string
    title: Snippet
    buttons?: Snippet
    children: Snippet
    onmove: (x: number, y: number, area: Area) => void
    onsize: (w: number, h: number, area: Area) => void
    onraise?: (() => void) | null
    // ondrag reports a press-and-drag in progress, so a caller can hold its saves.
    ondrag?: ((active: boolean) => void) | null
  }
  let { rect: r, area, label, z = 1, term, title, buttons, children, onmove, onsize, onraise = null, ondrag = null }: Props = $props()

  // track follows one pointer drag on the element that got the press.
  function track(e: PointerEvent, onstep: (m: PointerEvent) => void) {
    const el = e.currentTarget
    if (!(el instanceof HTMLElement)) return
    el.setPointerCapture(e.pointerId)
    ondrag?.(true)
    const up = () => { ondrag?.(false); el.removeEventListener('pointermove', onstep); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', up) }
    el.addEventListener('pointermove', onstep)
    el.addEventListener('pointerup', up)
    el.addEventListener('pointercancel', up)
  }

  function moveWin(e: PointerEvent) {
    if (e.target instanceof Element && e.target.closest('button')) return
    const dx = e.clientX - r.x, dy = e.clientY - r.y
    track(e, (m) => onmove(m.clientX - dx, m.clientY - dy, area))
  }

  function sizeWin(e: PointerEvent) {
    const x = e.clientX, y = e.clientY, w = r.w, h = r.h
    track(e, (m) => onsize(w + m.clientX - x, h + m.clientY - y, area))
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="win" data-term={term} role="group" aria-label={label} style:left="{r.x}px" style:top="{r.y}px" style:width="{r.w}px" style:height="{r.h}px" style:z-index={z} onpointerdowncapture={() => onraise?.()}>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="wtitle" onpointerdown={moveWin}>
    {@render title()}
    {#if buttons}<span class="wbtns">{@render buttons()}</span>{/if}
  </div>
  <!-- The body scrolls inside the window; a wheel or swipe over it stays here. -->
  <div class="wbody">{@render children()}</div>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="wsize" onpointerdown={sizeWin}></div>
</div>

<style>
  .win { position: absolute; pointer-events: auto; display: flex; flex-direction: column; min-width: 0; background: #19181a; border: 1px solid #45424a; border-radius: 8px; box-shadow: 0 12px 32px rgba(0, 0, 0, 0.55); overflow: hidden; }
  .wtitle { display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 var(--s1) 0 var(--s3); background: #232126; color: var(--ink-2); font-size: var(--t-sm); font-weight: 500; cursor: move; flex: none; touch-action: none; user-select: none; }
  .wtitle :global(.tl) { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .wtitle :global(.dot) { width: 6px; height: 6px; border-radius: 50%; background: var(--sage); flex: none; }
  .wbtns { display: flex; gap: 2px; flex: none; }
  .wbtns :global(.ib) { width: 22px; height: 22px; display: grid; place-items: center; border-radius: 5px; color: var(--muted); flex: none; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .wbtns :global(.ib:hover) { color: var(--ink); background: var(--hover); }
  }
  .wbtns :global(.ib:active) { background: var(--select); }
  .wbody { flex: 1; min-height: 0; display: flex; flex-direction: column; overscroll-behavior: contain; }
  .wsize { position: absolute; right: 0; bottom: 0; width: 16px; height: 16px; cursor: nwse-resize; touch-action: none; background: linear-gradient(135deg, transparent 55%, var(--faint) 55% 62%, transparent 62% 76%, var(--faint) 76% 83%, transparent 83%); opacity: 0.6; }
</style>
