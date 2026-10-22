<script>
  // The minimized call on a phone: a small circle with the mic, its live
  // state as a ring. Double-tap it to open the call, hold it to talk, and
  // drag it anywhere: it snaps to the nearest side and keeps its place. A badge counts the files
  // shared in the call and not yet opened; for a few seconds after one comes,
  // a one-line peek says what it is.
  import { untrack } from 'svelte'
  import { icons } from './icons.js'
  import { shareLine } from './conversation.js'
  import { bubbleGestures, bubbleOrb, bubblePlace, bubbleTop, snapBubble, BUBBLE, SHARE_PEEK } from './phone.js'

  // shared: the unseen shared items, oldest first.
  // ontalk(on) starts and ends a hold: it talks, and opens a muted mic
  // only while held.
  let { s, question = null, shared = [], onexpand, ontalk } = $props()

  const KEY = 'dyna.voice.bubble'
  let held = $state(false)
  let o = $derived(bubbleOrb(s, held))
  let place = $state(saved())
  function saved() {
    try { return bubblePlace(localStorage.getItem(KEY)) } catch { return bubblePlace(null) }
  }
  let drag = $state(null) // {x, y} of the finger while dragging

  // Only an item shared while the bubble shows gets a peek.
  let newest = $derived(shared.at(-1)?.key)
  let peeked = untrack(() => newest)
  let peek = $state('')
  $effect(() => {
    if (!newest || newest === peeked) {
      peek = ''
      return
    }
    peeked = newest
    peek = untrack(() => shareLine(shared.at(-1)))
    const t = setTimeout(() => (peek = ''), SHARE_PEEK)
    return () => clearTimeout(t)
  })

  const g = bubbleGestures({
    onmove: (p) => (drag = p),
    ondrop: (p) => {
      place = snapBubble({ ...p, width: window.innerWidth, height: window.innerHeight })
      drag = null
      try { localStorage.setItem(KEY, JSON.stringify(place)) } catch { /* private mode */ }
    },
    onexpand: () => onexpand(),
    ontalk: (on) => {
      held = on
      navigator.vibrate?.(10) // a light tick, where the browser has one
      ontalk(on)
    },
  })
  // A hold must end even when its pointerup never comes: the browser took
  // the pointer, the page lost focus or hid, or the bubble went away (a
  // rotation past the phone breakpoint) while the call goes on.
  function cancel(e) {
    if (g.cancel(e)) drag = null
  }
  $effect(() => () => cancel())
  function down(e) {
    if (g.down(e)) e.currentTarget.setPointerCapture(e.pointerId)
  }
  let style = $derived(drag
    ? `left:${drag.x - BUBBLE / 2}px;top:${drag.y - BUBBLE / 2}px`
    : `${place.side}:8px;top:${bubbleTop(place)}`)
</script>

<svelte:window onblur={() => cancel()} />
<svelte:document onvisibilitychange={() => document.hidden && cancel()} />

<button class="bubble {o.tone}" class:asks={!!question} class:dragging={!!drag} {style} aria-label="Voice call: {o.label}. Double-tap to open the call, hold to talk"
  onpointerdown={down} onpointermove={g.move} onpointerup={g.up} onpointercancel={cancel} onlostpointercapture={cancel} onclick={g.click} oncontextmenu={(e) => e.preventDefault()}>
  <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true"><path d={s.muted || s.deafened ? icons.micOff : icons.mic} fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
  {#if question}<span class="dot" aria-hidden="true"></span>{/if}
  {#if shared.length}<span class="badge" aria-label="{shared.length} shared">{shared.length}</span>{/if}
</button>
{#if peek && !drag}<p class="peek" role="status" style="{place.side}:{BUBBLE + 16}px;top:calc({place.top * 100}% - 16px)">{peek}</p>{/if}

<style>
  .bubble {
    --c: var(--muted);
    position: fixed;
    z-index: 60;
    width: 56px;
    height: 56px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    color: var(--ink);
    background: var(--panel);
    box-shadow: 0 0 0 3px var(--c), 0 6px 18px rgba(0, 0, 0, 0.45);
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
    -webkit-touch-callout: none;
    transition: left var(--motion-fast) var(--ease), right var(--motion-fast) var(--ease), top var(--motion-fast) var(--ease);
  }
  .bubble.dragging { transition: none; }
  .bubble.listening { --c: var(--sage); }
  .bubble.thinking { --c: var(--gold); }
  .bubble.speaking { --c: var(--iris); animation: ring 1.6s ease-out infinite; }
  .bubble.muted { --c: var(--coral); color: var(--coral); }
  .bubble.error { --c: var(--coral); }
  .dot { position: absolute; top: 2px; right: 2px; width: 12px; height: 12px; border-radius: 50%; background: var(--gold); box-shadow: 0 0 0 2px var(--panel); }
  .badge { position: absolute; left: -4px; bottom: -4px; min-width: 20px; height: 20px; padding: 0 4px; border-radius: 6px; display: grid; place-items: center; font-size: var(--t-cap); font-weight: 700; color: var(--rail); background: var(--iris); box-shadow: 0 0 0 2px var(--panel); }
  .peek { position: fixed; z-index: 60; margin: 0; max-width: min(240px, calc(100vw - 96px)); padding: 6px 10px; border-radius: var(--r-sm); background: var(--panel); border: 1px solid var(--line); box-shadow: 0 6px 18px rgba(0, 0, 0, 0.45); font-size: var(--t-sm); color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; pointer-events: none; }
  @keyframes ring { 50% { box-shadow: 0 0 0 6px var(--c), 0 6px 18px rgba(0, 0, 0, 0.45); } }
  @media (prefers-reduced-motion: reduce) { .bubble.speaking { animation: none; } }
</style>
