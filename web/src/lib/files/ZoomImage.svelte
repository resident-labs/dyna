<script lang="ts">
  import { FIT, MAX_SCALE, MIN_SCALE, actualScale, clampView, toggleZoom, wheelFactor, zoomAt } from './zoom.ts'
  import type { Point, Size, View } from './zoom.ts'

  interface Props {
    src: string
    alt: string
    onerror?: () => void
  }
  let { src, alt, onerror }: Props = $props()

  const STEP = 1.5
  const TAP_MOVE = 8
  const TAP_MS = 300

  let root: HTMLDivElement | undefined = $state()
  let stage: HTMLDivElement | undefined = $state()
  let img: HTMLImageElement | undefined = $state()
  let view = $state<View>(FIT)
  let panning = $state(false)
  let fullscreen = $state(false)
  let naturalWidth = $state(0)
  let naturalHeight = $state(0)
  let hovering = false

  const pointers = new Map<number, Point>()
  let pinchDistance = 0
  let dragMoved = 0
  let lastTap: { time: number; x: number; y: number } | null = null

  let stageSize = $state<Size>({ width: 0, height: 0 })
  const boxSize = (): Size => stageSize
  // Image size at fit: as large as the stage allows, never above its own pixels. CSS percentages cannot do this reliably in a flex or grid parent, so the size is set in pixels.
  const fitRatio = $derived(
    naturalWidth > 0 && naturalHeight > 0 && stageSize.width > 0 && stageSize.height > 0
      ? Math.min(1, stageSize.width / naturalWidth, stageSize.height / naturalHeight)
      : 1,
  )
  const contentSize = (): Size => ({ width: naturalWidth * fitRatio, height: naturalHeight * fitRatio })
  // A point relative to the center of the stage.
  function local(clientX: number, clientY: number): Point {
    const r = stage?.getBoundingClientRect()
    return r ? { x: clientX - r.left - r.width / 2, y: clientY - r.top - r.height / 2 } : { x: 0, y: 0 }
  }
  const percent = $derived(Math.round(view.scale * fitRatio * 100))

  function zoomBy(factor: number, at: Point = { x: 0, y: 0 }): void {
    view = zoomAt(view, factor, at, boxSize(), contentSize())
  }
  const reset = (): void => { view = FIT }

  function onwheel(e: WheelEvent): void {
    // Chrome sends a pinch as a wheel event with ctrlKey. Any wheel over the image zooms it, so the page never scrolls or zooms behind it.
    e.preventDefault()
    const dy = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY
    zoomBy(wheelFactor(Math.max(-120, Math.min(120, dy)), e.ctrlKey), local(e.clientX, e.clientY))
  }

  function onpointerdown(e: PointerEvent): void {
    stage?.setPointerCapture(e.pointerId)
    pointers.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (pointers.size === 1) { dragMoved = 0; panning = true }
    if (pointers.size === 2) pinchDistance = pointerDistance()
  }

  const ORIGIN: Point = { x: 0, y: 0 }
  function firstTwo(): [Point, Point] {
    const [a = ORIGIN, b = ORIGIN] = pointers.values()
    return [a, b]
  }
  function pointerDistance(): number {
    const [a, b] = firstTwo()
    return Math.hypot(a.x - b.x, a.y - b.y)
  }

  function onpointermove(e: PointerEvent): void {
    const prev = pointers.get(e.pointerId)
    if (!prev) return
    const next = { x: e.clientX, y: e.clientY }
    pointers.set(e.pointerId, next)
    if (pointers.size >= 2) {
      const distance = pointerDistance()
      const [a, b] = firstTwo()
      if (pinchDistance > 0) zoomBy(distance / pinchDistance, local((a.x + b.x) / 2, (a.y + b.y) / 2))
      pinchDistance = distance
      dragMoved = TAP_MOVE
      return
    }
    const dx = next.x - prev.x
    const dy = next.y - prev.y
    dragMoved += Math.abs(dx) + Math.abs(dy)
    if (view.scale > MIN_SCALE) view = clampView({ ...view, x: view.x + dx, y: view.y + dy }, boxSize(), contentSize())
  }

  function onpointerup(e: PointerEvent): void {
    if (!pointers.delete(e.pointerId)) return
    pinchDistance = 0
    if (pointers.size > 0) { dragMoved = TAP_MOVE; return }
    panning = false
    if (e.type !== 'pointerup' || dragMoved >= TAP_MOVE) { lastTap = null; return }
    // Pointer Events give one double-click/double-tap path for mouse and touch.
    const now = e.timeStamp
    if (lastTap && now - lastTap.time < TAP_MS && Math.hypot(e.clientX - lastTap.x, e.clientY - lastTap.y) < 24) {
      view = toggleZoom(view, local(e.clientX, e.clientY), boxSize(), contentSize(), actualScale(fitRatio))
      lastTap = null
    } else {
      lastTap = { time: now, x: e.clientX, y: e.clientY }
    }
  }

  // Block browser page zoom only while the pointer is over the viewer, and use the same keys for the image.
  function onkeydown(e: KeyboardEvent): void {
    if (!hovering || !(e.ctrlKey || e.metaKey)) return
    if (e.key === '+' || e.key === '=') zoomBy(STEP)
    else if (e.key === '-' || e.key === '_') zoomBy(1 / STEP)
    else if (e.key === '0') reset()
    else return
    e.preventDefault()
  }

  // Safari sends gesture events for trackpad pinch; they would zoom the page.
  const stopGesture = (e: Event): void => e.preventDefault()

  function wheelAction(node: HTMLElement) {
    node.addEventListener('wheel', onwheel, { passive: false })
    node.addEventListener('gesturestart', stopGesture)
    node.addEventListener('gesturechange', stopGesture)
    return {
      destroy: () => {
        node.removeEventListener('wheel', onwheel)
        node.removeEventListener('gesturestart', stopGesture)
        node.removeEventListener('gesturechange', stopGesture)
      },
    }
  }

  async function toggleFullscreen(): Promise<void> {
    try {
      if (document.fullscreenElement) await document.exitFullscreen()
      else await root?.requestFullscreen()
    } catch { /* The browser refused full screen; the viewer stays as it is. */ }
  }

  // A new image starts at fit. Resizing the window can leave the offset outside the new limits.
  $effect(() => { src; view = FIT })
  $effect(() => {
    const s = stage
    if (!s) return
    const measure = (): void => { stageSize = { width: s.clientWidth, height: s.clientHeight } }
    measure()
    const observer = new ResizeObserver(() => { measure(); view = clampView(view, boxSize(), contentSize()) })
    observer.observe(s)
    return () => observer.disconnect()
  })
</script>

<svelte:window {onkeydown} />
<svelte:document onfullscreenchange={() => { fullscreen = document.fullscreenElement === root }} />

<div class="zoom" bind:this={root}>
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="stage"
    class:zoomed={view.scale > MIN_SCALE}
    class:panning
    bind:this={stage}
    use:wheelAction
    {onpointerdown}
    {onpointermove}
    {onpointerup}
    onpointercancel={onpointerup}
    onpointerenter={() => { hovering = true }}
    onpointerleave={() => { hovering = false }}
  >
    <img
      bind:this={img}
      {src}
      {alt}
      draggable="false"
      style:transform="translate({view.x}px, {view.y}px) scale({view.scale})"
      style:width={naturalWidth > 0 ? `${naturalWidth * fitRatio}px` : undefined}
      style:height={naturalHeight > 0 ? `${naturalHeight * fitRatio}px` : undefined}
      onload={() => { naturalWidth = img?.naturalWidth ?? 0; naturalHeight = img?.naturalHeight ?? 0 }}
      {onerror}
    />
  </div>
  <div class="controls" role="group" aria-label="Image zoom">
    <button onclick={() => zoomBy(1 / STEP)} disabled={view.scale <= MIN_SCALE} aria-label="Zoom out" title="Zoom out">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M3 8h10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    </button>
    <span class="percent" role="status" aria-label="Zoom level">{percent}%</span>
    <button onclick={() => zoomBy(STEP)} disabled={view.scale >= MAX_SCALE} aria-label="Zoom in" title="Zoom in">
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M3 8h10M8 3v10" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg>
    </button>
    <button class="text" onclick={reset} disabled={view.scale === MIN_SCALE && view.x === 0 && view.y === 0} title="Fit the image to the window">Reset</button>
    {#if document.fullscreenEnabled}
      <button onclick={toggleFullscreen} aria-label={fullscreen ? 'Exit full screen' : 'Full screen'} title={fullscreen ? 'Exit full screen' : 'Full screen'}>
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
          {#if fullscreen}<path d="M6 2v4H2M10 2v4h4M6 14v-4H2M10 14v-4h4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          {:else}<path d="M2 6V2h4M14 6V2h-4M2 10v4h4M14 10v4h-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />{/if}
        </svg>
      </button>
    {/if}
  </div>
</div>

<style>
  .zoom { position: relative; flex: 1; min-height: 0; min-width: 0; display: flex; background: var(--rail); }
  .stage { flex: 1; min-height: 0; min-width: 0; display: grid; place-items: center; overflow: hidden; touch-action: none; user-select: none; overscroll-behavior: contain; cursor: zoom-in; }
  .stage.zoomed { cursor: grab; }
  .stage.panning.zoomed { cursor: grabbing; }
  img { display: block; max-width: none; max-height: none; flex: none; transform-origin: center; will-change: transform; -webkit-user-drag: none; }
  .controls { position: absolute; left: 50%; bottom: 14px; transform: translateX(-50%); display: flex; align-items: center; gap: 2px; padding: 4px; background: color-mix(in srgb, var(--side) 88%, transparent); border: 1px solid var(--line); border-radius: 10px; backdrop-filter: blur(8px); box-shadow: 0 4px 20px #0005; }
  .controls button { min-width: 36px; height: 36px; padding: 0 8px; display: grid; place-items: center; border-radius: 6px; color: var(--ink-2); font-size: 12px; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .controls button:disabled { opacity: 0.4; pointer-events: none; }
  .controls button:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 1px; }
  @media (hover: hover) {
    .controls button:hover { background: var(--hover); color: var(--ink); }
  }
  .controls button:active { background: var(--select); }
  .percent { min-width: 52px; text-align: center; font: 12px var(--mono); color: var(--muted); }
  @media (max-width: 600px) {
    .controls { bottom: 10px; }
    .controls button { min-width: 44px; height: 44px; }
  }
</style>
