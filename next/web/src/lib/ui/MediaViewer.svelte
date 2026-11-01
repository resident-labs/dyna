<script lang="ts">
  import { onMount } from "svelte";
  import Avatar from "./Avatar.svelte";
  import { formatTime } from "../feed.ts";
  import { FIT, clampView, toggleZoom, wheelFactor, zoomAt } from "../zoom.ts";
  import type { Point, Size, View } from "../zoom.ts";
  import type { MediaItem } from "../media.ts";

  let { items, initial, name, root, onclose, onjump }: {
    items: MediaItem[]; initial: number; name: (session: string) => string;
    root: (session?: string) => boolean; onclose: () => void; onjump: (key: string) => void;
  } = $props();
  let index = $state(initial), view = $state<View>(FIT), menu = $state(false);
  let dialog: HTMLDialogElement, stage: HTMLDivElement, img: HTMLImageElement;
  let natural = $state<Size>({ width: 0, height: 0 }), box = $state<Size>({ width: 0, height: 0 });
  let item = $derived(items[index]);
  let author = $derived(item.from.kind === "owner" ? "You" : item.from.session ? name(item.from.session) : "Dyna");
  let src = $derived(`/v1/files/${encodeURIComponent(item.file.sha256)}`);
  let fit = $derived(natural.width && natural.height ? Math.min(1, box.width / natural.width, box.height / natural.height) : 1);
  let content = $derived({ width: natural.width * fit, height: natural.height * fit });
  const pointers = new Map<number, Point>();
  let distance = 0, start: Point | undefined, moved = 0, lastTap = 0;
  const local = (x: number, y: number) => { const r = stage.getBoundingClientRect(); return { x: x - r.left - r.width / 2, y: y - r.top - r.height / 2 }; };
  function zoom(factor: number, at: Point = { x: 0, y: 0 }) { view = zoomAt(view, factor, at, box, content); }
  function move(n: number) { index = (index + n + items.length) % items.length; view = FIT; natural = { width: 0, height: 0 }; menu = false; }
  function key(e: KeyboardEvent) {
    if (e.target instanceof HTMLVideoElement || items.length < 2) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") { e.preventDefault(); move(e.key === "ArrowLeft" ? -1 : 1); }
  }
  function wheel(node: HTMLElement) {
    const handler = (e: WheelEvent) => { e.preventDefault(); zoom(wheelFactor(e.deltaY, e.ctrlKey), local(e.clientX, e.clientY)); };
    node.addEventListener("wheel", handler, { passive: false });
    return { destroy: () => node.removeEventListener("wheel", handler) };
  }
  function down(e: PointerEvent) {
    stage.setPointerCapture(e.pointerId); pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.size === 1) { start = { x: e.clientX, y: e.clientY }; moved = 0; }
    if (pointers.size === 2) { const [a, b] = [...pointers.values()]; distance = Math.hypot(a.x-b.x, a.y-b.y); }
  }
  function drag(e: PointerEvent) {
    const old = pointers.get(e.pointerId); if (!old) return;
    const next = { x: e.clientX, y: e.clientY }; pointers.set(e.pointerId, next);
    if (pointers.size === 2) {
      const [a, b] = [...pointers.values()]; const d = Math.hypot(a.x-b.x, a.y-b.y);
      if (distance) zoom(d / distance, local((a.x+b.x)/2, (a.y+b.y)/2));
      distance = d; moved = 100; return;
    }
    const dx = next.x-old.x, dy = next.y-old.y; moved += Math.abs(dx)+Math.abs(dy);
    if (view.scale > 1) view = clampView({ ...view, x: view.x+dx, y: view.y+dy }, box, content);
  }
  function up(e: PointerEvent) {
    if (!pointers.delete(e.pointerId)) return;
    distance = 0;
    if (pointers.size || !start) return;
    const dx = e.clientX-start.x, dy = e.clientY-start.y;
    if (e.pointerType === "touch" && view.scale === 1 && moved > 50) {
      if (dy > 80 && dy > Math.abs(dx)) dialog.close();
      else if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) && items.length > 1) move(dx > 0 ? -1 : 1);
    } else if (moved < 8) {
      if (e.timeStamp-lastTap < 300) { view = toggleZoom(view, local(e.clientX,e.clientY), box, content); lastTap = 0; }
      else lastTap = e.timeStamp;
    }
    start = undefined;
  }
  onMount(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.showModal();
    const observer = new ResizeObserver(() => { box = { width: stage.clientWidth, height: stage.clientHeight }; view = clampView(view, box, content); });
    observer.observe(stage);
    return () => { observer.disconnect(); if (opener?.isConnected) opener.focus(); };
  });
</script>

<dialog bind:this={dialog} aria-label="Media viewer" onclose={onclose} onkeydown={key} onclick={e => { if (e.target === dialog) dialog.close(); }}>
  <header>
    <button class="round phone" aria-label="Close" onclick={() => dialog.close()}>×</button>
    <Avatar kind={item.from.kind} name={author} root={root(item.from.session)} size={32} />
    <div class="identity"><strong>{author}</strong><span class="desktop"><time datetime={item.at}>{formatTime(item.at)}</time></span><span class="phone">{#if items.length > 1}{index + 1} of {items.length}{/if}</span></div>
    <button class="jump desktop" onclick={() => { onjump(item.key); dialog.close(); }}>Jump to post</button>
    <div class="spacer"></div>
    {#if item.file.media.startsWith("image/")}
      <div class="zoom desktop" role="group" aria-label="Image zoom">
        <button aria-label="Zoom out" onclick={() => zoom(1/1.5)}>−</button><button disabled={view.scale === 1} onclick={() => view = FIT}>Fit</button><button aria-label="Zoom in" onclick={() => zoom(1.5)}>+</button>
      </div>
    {/if}
    <a class="round" href={src} download={item.file.name} aria-label="Download" title="Download">↓</a>
    <a class="round desktop" href={src} target="_blank" rel="noopener" aria-label="Open original" title="Open original">↗</a>
    <button class="round desktop" aria-label="Close" onclick={() => dialog.close()}>×</button>
    <div class="more phone"><button class="round" aria-label="More options" aria-expanded={menu} onclick={() => menu = !menu}>⋯</button>
      {#if menu}<div class="menu"><a href={src} target="_blank" rel="noopener">Open original</a><button onclick={() => { onjump(item.key); dialog.close(); }}>Jump to post</button></div>{/if}
    </div>
  </header>
  <div class="stage" bind:this={stage} use:wheel onpointerdown={down} onpointermove={drag} onpointerup={up} onpointercancel={up} role="presentation">
    {#if item.file.media.startsWith("video/")}
      {#key src}<video {src} controls preload="metadata" autoplay></video>{/key}
    {:else}
      <img bind:this={img} {src} alt={item.file.name} draggable="false" onload={() => natural = { width: img.naturalWidth, height: img.naturalHeight }}
        style:width={natural.width ? `${content.width}px` : undefined} style:height={natural.height ? `${content.height}px` : undefined}
        style:transform="translate({view.x}px, {view.y}px) scale({view.scale})" />
    {/if}
  </div>
  {#if items.length > 1}<button class="round prev desktop" aria-label="Previous media" onclick={() => move(-1)}>‹</button><button class="round next desktop" aria-label="Next media" onclick={() => move(1)}>›</button>{/if}
  <footer>{item.file.name}{#if items.length > 1}<span> · {index + 1} of {items.length}</span>{/if}</footer>
</dialog>

<style>
  dialog { position: fixed; inset: 0; margin: 0; padding: 0; border: 0; max-width: none; max-height: none; width: 100%; height: 100dvh; background: var(--screen); color: var(--screen-text); overflow: hidden; }
  dialog::backdrop { background: var(--scrim); }
  header { position: absolute; z-index: 2; inset: 0 0 auto; height: 56px; display: flex; align-items: center; gap: 10px; padding: 0 16px; background: var(--screen-chip); }
  .identity { display: flex; align-items: baseline; gap: 10px; min-width: 0; font-size: 13px; }
  .identity strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  time, footer, .identity span { color: var(--screen-muted); }
  .spacer { flex: 1; }
  button, a { color: inherit; font: inherit; cursor: pointer; }
  button { border: 0; background: none; }
  button:focus-visible, a:focus-visible { outline: 2px solid var(--screen-focus); }
  .round { display: grid; place-items: center; width: 40px; height: 40px; flex: none; border-radius: 50%; background: var(--screen-chip); font-size: 22px; text-decoration: none; }
  .round:hover, .zoom button:hover, .menu a:hover, .menu button:hover { background: var(--screen-hover); }
  .jump { font-size: 12px; color: var(--screen-muted); }
  .zoom { display: flex; align-items: center; background: var(--screen-chip); border: 1px solid var(--screen-line); border-radius: 20px; }
  .zoom button { min-width: 36px; height: 36px; }
  button:disabled { opacity: .45; }
  .stage { position: absolute; inset: 56px 0 72px; display: grid; place-items: center; overflow: hidden; touch-action: none; user-select: none; }
  img, video { display: block; max-width: 92vw; max-height: calc(100dvh - 128px); object-fit: contain; }
  img { max-width: none; max-height: none; transform-origin: center; }
  .prev, .next { position: absolute; top: 50%; transform: translateY(-50%); }
  .prev { left: 16px; } .next { right: 16px; }
  footer { position: absolute; bottom: 20px; width: 100%; text-align: center; font-size: 12px; pointer-events: none; }
  .phone { display: none; }
  @media (max-width: 640px) {
    header { padding: env(safe-area-inset-top) 8px 0; height: calc(56px + env(safe-area-inset-top)); gap: 4px; }
    .desktop { display: none; } .phone { display: block; } .round { width: 44px; height: 44px; }
    .identity { display: grid; gap: 1px; } .identity span { font-size: 11px; }
    .more { position: relative; } .menu { position: absolute; right: 0; top: 44px; min-width: 150px; padding: 4px; background: var(--screen-chip); border: 1px solid var(--screen-line); border-radius: 8px; }
    .menu a, .menu button { display: block; width: 100%; box-sizing: border-box; padding: 12px; text-align: left; text-decoration: none; font-size: 13px; }
    footer { bottom: calc(16px + env(safe-area-inset-bottom)); }
  }
</style>
