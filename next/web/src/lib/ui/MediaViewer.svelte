<script lang="ts">
  import { onMount } from "svelte";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import VisualCardMedia from "./VisualCardMedia.svelte";
  import type { VisualVersion } from "../visual.ts";
  import { call } from "../client.ts";
  import { formatTime } from "../feed.ts";
  import { FIT, actualScale, clampView, toggleZoom, wheelFactor, zoomAt } from "../zoom.ts";
  import type { Point, Size, View } from "../zoom.ts";
  import type { MediaItem } from "../media.ts";

  let { items, initial, name, root, room, loadVisual, onclose, onjump }: {
    items: MediaItem[]; initial: number; name: (session: string) => string;
    root: (session?: string) => boolean; room: string; loadVisual: (item: MediaItem) => Promise<VisualVersion[]>; onclose: () => void; onjump: (key: string) => void;
  } = $props();
  let selected = $state(items[initial]), view = $state<View>(FIT), menu = $state(false);
  let dialog: HTMLDialogElement, stage: HTMLDivElement, closer: HTMLButtonElement;
  let natural = $state<Size>({ width: 0, height: 0 }), box = $state<Size>({ width: 0, height: 0 });
  let found = $derived(items.findIndex(entry => entry.key === selected.key && entry.position === selected.position));
  let index = $derived(Math.max(0, found));
  let item = $derived(found >= 0 ? items[found] : selected);
  let author = $derived(item.from.kind === "owner" ? "you" : item.from.session ? name(item.from.session) : "Dyna");
  let versions = $state<VisualVersion[]>([]), visualFailed = $state(false), visualAttempt = $state(0);
  $effect(() => {
    const selected = item; visualAttempt;
    if (!selected.file.visual) { versions = []; return; }
    let active = true; versions = []; visualFailed = false;
    void loadVisual(selected).then(result => { if (active) { versions = result; visualFailed = !result.length; } }, () => { if (active) visualFailed = true; });
    return () => { active = false; };
  });
  let src = $derived(`/v1/files/${encodeURIComponent(item.file.sha256)}`);
  let original = $derived(item.file.visual ? undefined : src);
  let fit = $derived(natural.width && natural.height ? Math.min(1, box.width / natural.width, box.height / natural.height) : 1);
  let content = $derived({ width: natural.width * fit, height: natural.height * fit });
  const pointers = new Map<number, Point>();
  // outside: the press began on the scrim, so a drag that started in the panel and ended out there does not close it.
  let distance = 0, start: Point | undefined, moved = 0, lastTap = 0, background = false, outside = false;
  const local = (x: number, y: number) => { const r = stage.getBoundingClientRect(); return { x: x - r.left - r.width / 2, y: y - r.top - r.height / 2 }; };
  function zoom(factor: number, at: Point = { x: 0, y: 0 }) { view = zoomAt(view, factor, at, box, content); }
  function move(n: number) { background = false; if (!items.length) return; selected = items[found < 0 ? (n > 0 ? 0 : items.length - 1) : (found + n + items.length) % items.length]; view = FIT; natural = { width: 0, height: 0 }; menu = false; }
  function key(e: KeyboardEvent) {
    if (e.target instanceof HTMLVideoElement || e.target instanceof HTMLIFrameElement || items.length < 2) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") { e.preventDefault(); move(e.key === "ArrowLeft" ? -1 : 1); }
  }
  function wheel(node: HTMLElement) {
    const handler = (e: WheelEvent) => { if (item.file.visual || !item.file.media.startsWith("image/")) return; e.preventDefault(); zoom(wheelFactor(e.deltaY, e.ctrlKey), local(e.clientX, e.clientY)); };
    node.addEventListener("wheel", handler, { passive: false });
    return { destroy: () => node.removeEventListener("wheel", handler) };
  }
  function down(e: PointerEvent) {
    background = false;
    if (item.file.visual || e.target instanceof HTMLVideoElement) return;
    background = e.target === stage;
    stage.setPointerCapture(e.pointerId); pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (pointers.size === 1) { start = { x: e.clientX, y: e.clientY }; moved = 0; }
    if (pointers.size === 2) { start = undefined; const [a, b] = [...pointers.values()]; distance = Math.hypot(a.x-b.x, a.y-b.y); }
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
      if (e.timeStamp-lastTap < 300) { view = toggleZoom(view, local(e.clientX,e.clientY), box, content, actualScale(fit)); lastTap = 0; }
      else lastTap = e.timeStamp;
    }
    start = undefined;
  }
  onMount(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.showModal();
    closer.focus();
    const observer = new ResizeObserver(() => { box = { width: stage.clientWidth, height: stage.clientHeight }; view = clampView(view, box, content); });
    observer.observe(stage);
    return () => { observer.disconnect(); if (opener?.isConnected) opener.focus(); };
  });
</script>

<svelte:window onkeydown={key} />

<!-- The dialog is the whole window and shows the app behind it; anything outside the panel closes it. -->
<dialog class="media-viewer" bind:this={dialog} aria-label="Media viewer" onclose={onclose} onpointerdown={e => { outside = e.target === dialog; }} onclick={e => { if (outside && e.target === dialog) dialog.close(); }}>
  <div class="panel">
    <header>
      {#snippet identity()}
        <Avatar kind={item.from.kind} name={author} root={root(item.from.session)} size={24} />
        <span class="identity"><strong>{author}</strong><time datetime={item.at}>{formatTime(item.at)}</time></span>
      {/snippet}
      {#if item.from.session}<a class="sender" href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.from.session)}`}>{@render identity()}</a>
      {:else}<div class="sender">{@render identity()}</div>{/if}
      <button class="jump desktop" onclick={() => { onjump(item.key); dialog.close(); }}>Jump to post</button>
      <div class="spacer"></div>
      {#if !item.file.visual}
      {#if item.file.media.startsWith("image/")}
        <div class="zoom desktop" role="group" aria-label="Image zoom">
          <button aria-label="Zoom out" title="Zoom out" onclick={() => zoom(1/1.5)}><Icon name="minus" /></button><button class="fit" disabled={view.scale === 1} onclick={() => view = FIT}>Fit</button><button aria-label="Zoom in" title="Zoom in" onclick={() => zoom(1.5)}><Icon name="plus" /></button>
        </div>
      {/if}
      <a class="tool" href={src} download={item.file.name} aria-label="Download" title="Download"><Icon name="download" /></a>
      <a class="tool desktop" href={src} target="_blank" rel="noopener" aria-label="Open original" title="Open original"><Icon name="external" /></a>
      {/if}
      <div class="more phone"><button class="tool" aria-label="More options" aria-expanded={menu} onclick={() => menu = !menu}><Icon name="more" /></button>
        {#if menu}<div class="menu">{#if original}<a href={original} target="_blank" rel="noopener">Open original</a>{/if}<button onclick={() => { onjump(item.key); dialog.close(); }}>Jump to post</button></div>{/if}
      </div>
      <button class="tool" aria-label="Close" title="Close" onclick={() => dialog.close()} bind:this={closer}><Icon name="close" /></button>
    </header>
    <div class="stage" class:media-gesture={!item.file.visual} bind:this={stage} use:wheel onpointerdown={down} onpointermove={drag} onpointerup={up} onpointercancel={up} onclick={e => { if (background && e.target === stage && moved < 8) dialog.close(); }} role="presentation">
      {#if item.file.visual}
        <div class="visual-stage">{#if visualFailed}<div class="visual-error">The card did not load. <button onclick={() => visualAttempt++}>Retry</button></div>
        {:else if versions.length}{#key item.key + ":" + item.file.visual.version}<VisualCardMedia {versions} selected={item.file.visual.version} room={item.room ?? room} send={async (text, event) => { await call("post", { room: item.room ?? room, text, visualReply: event }); }} />{/key}{/if}</div>
      {:else if item.file.media.startsWith("video/")}
        {#key src}<video {src} controls preload="metadata" autoplay></video>{/key}
      {:else}
        <img {src} alt={item.file.name} draggable="false" onload={event => { if (event.currentTarget instanceof HTMLImageElement) natural = { width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight }; }}
          style:width={natural.width ? `${content.width}px` : undefined} style:height={natural.height ? `${content.height}px` : undefined}
          style:transform="translate({view.x}px, {view.y}px) scale({view.scale})" />
      {/if}
    </div>
    {#if !item.file.visual || items.length > 1}<footer><span class="name">{#if !item.file.visual}{item.file.name}{/if}</span>{#if items.length > 1}<span class="count">{#if !item.file.visual}{" · "}{/if}{index + 1} of {items.length}</span>{/if}</footer>{/if}
  </div>
  {#if items.length > 1}<button class="arrow previous" aria-label="Previous media" title="Previous" onclick={() => move(-1)}><Icon name="backChevron" size={20} /></button><button class="arrow following" aria-label="Next media" title="Next" onclick={() => move(1)}><Icon name="forwardChevron" size={20} /></button>{/if}
</dialog>

<style>
  /* The app behind stays in view, dimmed here and blurred by app.css. */
  dialog[open] { position: fixed; inset: 0; display: grid; place-items: center; width: 100%; height: 100dvh; max-width: none; max-height: none; margin: 0; padding: 32px 72px; border: 0; background: none; color: var(--text); overflow: hidden; }
  dialog::backdrop { background: var(--soft-scrim); }
  .panel { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; width: min(100%, 1280px); height: min(100%, 900px); min-width: 0; border: 1px solid var(--line); border-radius: 16px; background: var(--raised); box-shadow: 0 16px 48px var(--shadow-lg); overflow: hidden; }
  header { position: relative; z-index: 1; display: flex; align-items: center; gap: 8px; height: 48px; padding: 0 8px 0 16px; border-bottom: 1px solid var(--line); }
  .identity { display: flex; align-items: baseline; gap: 8px; min-width: 0; font-size: 13px; }
  .sender { display: flex; align-items: center; gap: 8px; min-width: 0; color: inherit; text-decoration: none; } a.sender:hover strong { text-decoration: underline; }
  .identity strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  time { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .spacer { flex: 1; }
  button, a { color: inherit; font: inherit; cursor: pointer; }
  button { padding: 0; border: 0; background: none; }
  button:focus-visible, a:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .tool { display: grid; place-items: center; flex: none; width: 32px; height: 32px; border-radius: 6px; color: var(--muted); text-decoration: none; }
  .tool:hover, .zoom button:hover:enabled, .jump:hover, .menu a:hover, .menu button:hover { background: var(--hover); color: var(--text); }
  .jump { flex: none; height: 28px; padding: 0 8px; border-radius: 6px; color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  .zoom { display: flex; align-items: center; margin-right: 4px; }
  .zoom button { display: grid; place-items: center; min-width: 32px; height: 32px; border-radius: 6px; color: var(--muted); }
  .zoom .fit { padding: 0 4px; font-size: 13px; font-weight: 500; }
  button:disabled { opacity: .45; cursor: default; }
  .stage { position: relative; display: grid; grid-template: minmax(0, 1fr) / minmax(0, 1fr); place-items: center; min-height: 0; overflow: hidden; background: var(--sidebar); }
  .stage.media-gesture { touch-action: none; user-select: none; }
  img, video { display: block; object-fit: contain; }
  video { max-width: 100%; max-height: 100%; }
  img { transform-origin: center; }
  .visual-stage { width: 100%; height: 100%; overflow: hidden; }
  .visual-error { padding: 24px; color: var(--muted); font-size: 13px; }
  .visual-error button { text-decoration: underline; }
  footer { display: flex; justify-content: center; align-items: center; min-width: 0; height: 36px; padding: 0 16px; border-top: 1px solid var(--line); color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  .name { overflow: hidden; text-overflow: ellipsis; }
  .count { flex: none; white-space: pre; font-variant-numeric: tabular-nums; }
  /* The arrows flank the panel on the scrim, so they never cover the media. */
  .arrow { position: absolute; top: 50%; display: grid; place-items: center; width: 40px; height: 40px; border: 1px solid var(--line); border-radius: 50%; background: var(--raised); color: var(--muted); transform: translateY(-50%); }
  .arrow:hover { background: linear-gradient(var(--hover) 0 0), var(--raised); color: var(--text); }
  .previous { left: max(16px, 50% - 696px); } .following { right: max(16px, 50% - 696px); }
  .phone { display: none; }
  @media (prefers-reduced-motion: no-preference) {
    dialog::backdrop { animation: settle 200ms ease-out; }
    .panel { animation: rise 200ms ease-out; }
    .arrow { animation: settle 200ms ease-out; }
  }
  @keyframes settle { from { opacity: 0; } }
  @keyframes rise { from { opacity: 0; transform: translateY(8px) scale(.99); } }
  /* A phone keeps a margin of the app on every side, so the viewer still reads as a sheet. The arrows move into the footer. */
  @media (max-width: 640px) {
    dialog[open] { padding: calc(12px + env(safe-area-inset-top)) 12px calc(12px + env(safe-area-inset-bottom)); }
    .panel { width: 100%; height: 100%; border-radius: 12px; }
    header { gap: 4px; height: 52px; padding: 0 4px 0 12px; }
    .desktop { display: none; } .phone { display: block; }
    .tool { width: 40px; height: 40px; }
    .identity { display: grid; gap: 0; } .identity time { font-size: 11px; }
    .more { position: relative; }
    .menu { position: absolute; right: 0; top: 44px; min-width: 150px; padding: 4px; border: 1px solid var(--line); border-radius: 6px; background: var(--raised); box-shadow: 0 8px 24px var(--shadow-lg); }
    .menu a, .menu button { display: block; width: 100%; padding: 12px; border-radius: 4px; font-size: 13px; text-align: left; text-decoration: none; }
    footer { height: 48px; padding: 0 56px; }
    .arrow { top: auto; bottom: calc(16px + env(safe-area-inset-bottom)); border: 0; background: none; transform: none; }
  }
</style>
