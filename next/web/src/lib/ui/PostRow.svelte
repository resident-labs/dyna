<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import Markdown from "./Markdown.svelte";
  import PostAttachments from "./PostAttachments.svelte";
  import { clock, formatTime, fullTime, type Row } from "../feed.ts";
  import type { Attachment, EventID, VisualCard } from "../contract.gen.ts";
  import { hue } from "../identity.ts";
  import { beginSwipe, moveSwipe, releaseSwipe, ignoredSwipeTarget, type Swipe } from "../swipe.ts";
  import { canForwardPost } from "../forward-state.ts";
  import { SwipeForward } from "../swipe-forward.ts";
  let { row, author, to, root, room, navigate, onretry, ondiscard, onforward, swipeDisabled = false, belowNew = false, newest, visualEvent, onmedia }: {
    room: string; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; row: Extract<Row, { kind: "post" }>; author: string; to?: string; root: boolean; onretry: (id: number) => void; ondiscard: (id: number) => void; onforward?: () => void; swipeDisabled?: boolean; belowNew?: boolean; newest: Map<string, VisualCard>; visualEvent: EventID | undefined; onmedia: (position: number) => void;
  } = $props();
  let distance = $state(0), dragging = $state(false), ready = $state(false);
  function swipe(node: HTMLElement) {
    let state: Swipe | null = null, id = -1, suppress = 0;
    let viewportListener: (() => void) | undefined;
    const delayedForward = new SwipeForward();
    function reset() { state = null; dragging = false; distance = 0; ready = false; }
    function start(event: TouchEvent) {
      reset();
      if (event.touches.length !== 1 || !onforward || swipeDisabled || (!row.text.trim() && !row.attachments.length)) return;
      const touch = event.touches[0], target = event.target;
      if (!(target instanceof Element)) return;
      state = beginSwipe(touch.clientX, touch.clientY, performance.now(), matchMedia("(pointer: coarse)").matches,
        !!target.closest(ignoredSwipeTarget), !!getSelection()?.toString());
      id = touch.identifier;
    }
    function move(event: TouchEvent) {
      if (!state) return;
      const touch = event.touches[0];
      if (event.touches.length !== 1 || touch.identifier !== id) { reset(); return; }
      const result = moveSwipe(state, touch.clientX, touch.clientY, performance.now(), event.cancelable, !!getSelection()?.toString());
      state = result.swipe;
      if (!state) { reset(); return; }
      if (!state.dragging) return;
      event.preventDefault();
      dragging = true;
      distance = row.pending ? Math.min(12, result.distance) : result.distance;
      ready = !row.pending && touch.clientX - state.x >= 64;
      if (result.tick && !row.pending) navigator.vibrate?.(10);
    }
    function end(event: TouchEvent) {
      if (!state) return;
      const touch = [...event.changedTouches].find(item => item.identifier === id);
      const forward = touch && canForwardPost(!!row.pending, swipeDisabled, row.text, row.attachments.length) && event.touches.length === 0 && releaseSwipe(state, touch.clientX);
      if (state.dragging) { suppress = Date.now() + 500; if (event.cancelable) event.preventDefault(); }
      reset();
      if (forward) delayedForward.start(() => {
        onforward?.();
        const view = node.closest(".virtual"), viewport = visualViewport;
        if (view instanceof HTMLElement) {
          const reveal = () => {
            const bottom = Math.min(view.getBoundingClientRect().bottom, viewport ? viewport.offsetTop + viewport.height : innerHeight);
            const overflow = node.getBoundingClientRect().bottom - bottom;
            if (overflow > 0) view.scrollTop += overflow;
          };
          if (viewportListener) viewport?.removeEventListener("resize", viewportListener);
          viewportListener = reveal;
          viewport?.addEventListener("resize", reveal, { once: true });
          requestAnimationFrame(reveal);
        }
      }, matchMedia("(prefers-reduced-motion: reduce)").matches ? 0 : 150);
    }
    function click(event: MouseEvent) { if (Date.now() < suppress) { event.preventDefault(); event.stopImmediatePropagation(); } }
    function enter(event: PointerEvent) { raise(event, node); }
    node.addEventListener("pointerenter", enter);
    node.addEventListener("touchstart", start, { passive: true });
    node.addEventListener("touchmove", move, { passive: false });
    node.addEventListener("touchend", end, { passive: false });
    node.addEventListener("touchcancel", reset);
    node.addEventListener("contextmenu", reset);
    node.addEventListener("click", click, true);
    return { destroy() {
      node.removeEventListener("pointerenter", enter);
      node.removeEventListener("touchstart", start); node.removeEventListener("touchmove", move);
      node.removeEventListener("touchend", end); node.removeEventListener("touchcancel", reset);
      node.removeEventListener("contextmenu", reset); node.removeEventListener("click", click, true);
      delayedForward.cancel();
      if (viewportListener) visualViewport?.removeEventListener("resize", viewportListener);
    } };
  }
  // Copy text answers in the bar for 2 seconds, as in v1.
  let copied = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  function copy() {
    clearTimeout(timer);
    navigator.clipboard.writeText(row.text).then(() => { copied = "Copied"; }, () => { copied = "Could not copy"; })
      .finally(() => { timer = setTimeout(() => { copied = ""; }, 2000); });
  }
  // The 34 px bar straddles the row's top edge, or sits 4 px inside it right under the New line. When the row's top is
  // under the feed's top edge, a pointer entering it lifts the bar to 4 px inside that edge, measured once per entry, never
  // on scroll. The cap keeps it from leaving a row that is almost scrolled away, and lets a one-line row clear the edge.
  let lift = $state(0);
  function raise(event: PointerEvent, node: HTMLElement) {
    if (event.pointerType === "touch") return;
    // The feed's scroll container: VirtualList.svelte line 13 defaults role to "feed", and line 140 renders it on .virtual.
    const edge = node.closest('[role="feed"]')?.getBoundingClientRect().top ?? 0;
    const top = belowNew ? 4 : -17;
    lift = Math.min(Math.max(0, edge + 4 - (node.getBoundingClientRect().top + top)), node.offsetHeight - 4 - top);
  }
  // A touch held for 450 ms without moving 10 px opens the sheet.
  let sheet = $state(false);
  let held: { timer: ReturnType<typeof setTimeout>; x: number; y: number } | undefined;
  function press(event: PointerEvent) {
    if (event.pointerType !== "touch" || row.pending) return;
    held = { x: event.clientX, y: event.clientY, timer: setTimeout(() => { getSelection()?.removeAllRanges(); sheet = true; held = undefined; }, 450) };
  }
  function drift(event: PointerEvent) {
    if (held && Math.hypot(event.clientX - held.x, event.clientY - held.y) > 10) release();
  }
  function release() {
    if (held) clearTimeout(held.timer);
    held = undefined;
  }
</script>

<!-- A long press opens the action sheet; a right swipe forwards on release. -->
<div class="swipe" class:mention={row.from.kind !== "owner" && row.to.some(target => target.kind === "owner")} use:swipe>
  {#if dragging && !row.pending}<div class="cue" class:ready class:first={row.first} aria-hidden="true"><Icon name="forward" /></div>{/if}
<article style:transform="translateX({distance}px)" class:dragging class="post" class:first={row.first} class:tucked={belowNew} class:pending={row.pending} aria-busy={row.pending && !row.pending.failed} tabindex="-1"
  style:--lift="{lift}px" onpointerdown={press} onpointermove={drift} onpointerup={release} onpointercancel={release}
  oncontextmenu={event => { if (sheet) event.preventDefault(); }}>
  {#if !row.pending}<div class="bar"><span class="status" role="status">{copied}</span>
    {#if onforward}<button type="button" aria-label="Forward" title="Forward" onclick={onforward}><Icon name="forward" size={18} /></button>{/if}
    <button type="button" aria-label="Copy text" title="Copy text" onclick={copy}><Icon name={copied === "Copied" ? "check" : "copy"} size={18} /></button></div>{/if}
  {#if sheet}<dialog class="sheet" aria-label="Post actions" {@attach (dialog: HTMLDialogElement) => dialog.showModal()} onclose={() => { sheet = false; }}
    onclick={event => { if (event.target === event.currentTarget) event.currentTarget.close(); }}>
    {#if onforward}<button type="button" onclick={event => { event.currentTarget.closest("dialog")?.close(); onforward(); }}><Icon name="forward" />Forward</button>{/if}
    <button type="button" onclick={event => { event.currentTarget.closest("dialog")?.close(); copy(); }}><Icon name="copy" />Copy text</button></dialog>{/if}
  {#if row.first}
    <Avatar kind={row.from.kind} name={author} {root} size={40} />
    <p class="name">{#if row.from.session}<a href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(row.from.session)}`} onclick={navigate} style:--hue={hue(author)}>{author}</a>{:else}<strong class={row.from.kind} style:--hue={hue(author)}>{author}</strong>{/if}
      <time datetime={row.at} title={fullTime(row.at)}>{formatTime(row.at)}</time>{#if to}<span class="to">→ {to}</span>{/if}</p>
  {:else}
    <time class="follow-time" datetime={row.at} title={fullTime(row.at)}>{clock(row.at)}</time>
  {/if}
  <Markdown text={row.text} />
  <PostAttachments {onmedia} attachments={row.attachments} {newest} event={visualEvent} at={row.at} />
  {#if row.pending?.failed}{@const id = row.pending.id}<p class="unsent" role="alert">Not sent
    <button type="button" onclick={() => onretry(id)}>Retry</button><button type="button" onclick={() => ondiscard(id)}>Discard</button></p>{/if}
</article>
</div>

<style>
  /* The post stays in the feed's capped column, and the row around it reaches the pane's edges, so a hover or mention wash runs edge to edge. */
  .swipe { --bleed: max(0px, (100cqw - var(--feed-reading-width)) / 2); position: relative; margin-inline: calc(-1 * var(--bleed)); padding-inline: var(--bleed); }
  @media (pointer: coarse) { .swipe { overflow-x: clip; } }
  .cue { position: absolute; left: calc(var(--bleed) + 12px); top: 22px; z-index: 1; display: grid; place-items: center; width: 34px; height: 34px; border-radius: 50%; background: var(--selected); color: var(--muted); opacity: .4; transform: scale(.8); }
  .cue:not(.first) { top: 15px; }
  .cue.ready { background: var(--focus); color: var(--bg); opacity: 1; transform: scale(1.15); }
  .post { transition: transform 150ms ease; }
  .post.dragging { transition: none; }
  @media (prefers-reduced-motion: reduce) { .post { transition: none; } }
  /* The avatar sits in a 40 px gutter; later posts of a run align under the first one's text. */
  .post { position: relative; display: grid; grid-template-columns: 40px minmax(0, 1fr); column-gap: 16px; padding: 1px 16px; font-size: 15px; }
  /* The gap above a run is inside the row, so washes on neighboring rows meet with no band between them. */
  .post.first { padding-top: 17px; }
  /* The wash follows the pointer only; a tap or a closed menu leaves none behind. */
  @media (hover: hover) { .swipe:hover { background: var(--post-hover); } }
  .mention { background: var(--mention-bg); }
  .mention .post { padding-top: 4px; padding-bottom: 4px; }
  .mention .post.first { padding-top: 20px; }
  .mention::before { content: ""; position: absolute; inset: 0 auto 0 0; width: 2px; background: var(--mention-bar); }
  @media (hover: hover) { .swipe.mention:hover { background: linear-gradient(var(--post-hover) 0 0), var(--mention-bg); } }
  .post .follow-time { grid-column: 1; grid-row: 1; align-self: start; text-align: center; font-size: 11px; visibility: hidden; }
  @media (hover: hover) { .swipe:hover .follow-time { visibility: visible; } }
  @media (max-width: 640px) { .post { column-gap: 8px; } }
  .post:focus { outline: none; }
  .post > :global(*) { grid-column: 2; }
  /* A slim bar, as in Discord: it straddles the row's top right and is laid out while hidden, so showing it never moves the row. */
  .bar { position: absolute; top: calc(-17px + var(--lift, 0px)); right: 16px; z-index: 1; display: flex; align-items: center; gap: 2px; height: 34px; padding: 2px;
    background: var(--raised); border: 1px solid var(--firm); border-radius: 8px; box-shadow: 0 2px 8px var(--shadow-sm); opacity: 0; pointer-events: none; }
  .tucked .bar { top: calc(4px + var(--lift, 0px)); }
  /* Hidden by opacity, not visibility, so Tab still reaches the buttons and shows the bar. A tap focuses the row but is not focus-visible. */
  @media (hover: hover) { .swipe:hover .bar { opacity: 1; pointer-events: auto; } }
  .post:has(:focus-visible) .bar { opacity: 1; pointer-events: auto; }
  .bar button { all: unset; display: grid; place-items: center; width: 30px; height: 28px; border-radius: 6px; color: var(--text); opacity: .8; cursor: pointer; }
  .bar button:hover { background: var(--selected); opacity: 1; }
  .bar button:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The touch sheet: full width at the bottom, 44 px rows, closed by a tap outside or Esc. */
  .sheet { position: fixed; inset: auto 0 0; margin: 0; width: 100%; max-width: none; padding: 8px 8px max(8px, env(safe-area-inset-bottom)); border: 0;
    border-top: 1px solid var(--line); border-radius: 12px 12px 0 0; background: var(--raised); color: var(--text); }
  .sheet::backdrop { background: var(--scrim); }
  .sheet button { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 12px; width: 100%; height: 44px; padding: 0 12px; border-radius: 8px; cursor: pointer; }
  .sheet button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .status { color: var(--muted); font-size: 12px; }
  .status:not(:empty) { padding: 0 6px; }
  .post > :global(.avatar) { grid-column: 1; grid-row: 1 / span 3; margin-top: 2px; }

  /* A post the engine has not confirmed is dimmed; its Not sent line keeps full contrast in the danger color, because the owner must act. */
  .pending > :global(:not(.unsent, .sheet)) { opacity: 0.5; }
  .unsent { margin: 2px 0 0; display: flex; align-items: baseline; gap: 12px; color: var(--danger); font-size: 12px; }
  .unsent button { all: unset; cursor: pointer; color: var(--focus); border-radius: 3px; }
  .unsent button:hover { text-decoration: underline; }
  .unsent button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* The name line stays one line: a long name or recipient list truncates and the time stays whole. */
  .name { margin: 0; display: flex; align-items: baseline; gap: 8px; white-space: nowrap; }
  strong, .name a { flex: none; max-width: 60%; overflow: hidden; text-overflow: ellipsis; color: oklch(var(--id-name) var(--hue)); }
  .name a { text-decoration: none; font-weight: 600; }
  .name a:hover { text-decoration: underline; }
  .name a:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  time { flex: none; }
  /* A name stays whole up to 60% of the line, so "You" never truncates; the recipient list takes the rest. */
  .to { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  strong.owner { color: var(--focus); }
  strong.engine { color: var(--text); }
  time, .to { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
