<script lang="ts">
  // The card for a hook run: one quiet line in the feed and in a session DM,
  // with the state of the run, how long it took and a way to its log. It reads
  // the typed payload of the post, never its text, and keeps no feed-only
  // state. The start of a run folds into its result, and repeated passes of one
  // hook fold into one row: the card then says how many passes it holds and
  // when the last one ran, and View log opens that last run.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { hookEvent } from './eventPayloads.ts'
  import type { HookState } from './events.gen.ts'
  import type { HookFold } from './feedRows.ts'
  import { openHookLog } from '../settings/hookLog.ts'
  import EventDetails from './EventDetails.svelte'
  import HookIcon from '../ui/HookIcon.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed', hookFold }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    hookFold?: HookFold
  } = $props()

  const stateWords: Record<HookState, string> = {
    running: 'running',
    passed: 'passed',
    failed: 'failed',
    error: 'error',
  }
  // A failed hook and a hook that could not run both need the owner's eye.
  const stateTone: Record<HookState, string> = {
    running: 'gold',
    passed: 'sage',
    failed: 'coral',
    error: 'coral',
  }

  let payload = $derived(hookEvent(post))
  let name = $derived(payload?.label || payload?.hook || 'Hook')
  let state = $derived<HookState>(payload?.state ?? 'running')
  // A folded row counts its passes here; a row of one run shows only its own
  // time, so a pass that folded once does not claim a count.
  let count = $derived(hookFold?.count ?? 1)
  // A run under a second reads in milliseconds, so a quick hook does not look
  // like a clock that failed to move. A fold shows the time of its last pass
  // instead.
  let elapsed = $derived.by(() => {
    const ms = payload?.durationMs ?? 0
    if (ms <= 0) return ''
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
  })

  function viewLog() {
    if (!payload) return
    openHookLog({ repo: payload.repo, hook: payload.hook, runId: payload.runId })
  }
</script>

<div class="card {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph {stateTone[state]}"><HookIcon size={15} /></span>
    <span class="title">
      <span class="name">{name}</span>
      <span class="state {stateTone[state]}">{stateWords[state]}</span>
    </span>
    {#if count > 1 && hookFold}
      <span class="fact" title={new Date(hookFold.lastTs).toLocaleString()}>{count} times · last {clock(hookFold.lastTs)}</span>
    {:else if elapsed}
      <span class="fact">{elapsed}</span>
    {/if}
    <button class="btn" type="button" onclick={viewLog} aria-label={count > 1 ? `View the log of the last run of ${name}` : `View the log of ${name}`}>View log</button>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.detail}<p class="detail">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    color: var(--ink-2);
  }
  /* The glyph is an icon, so it takes the state's colour like the other
     cards' glyphs do. */
  .glyph { flex: none; display: grid; place-items: center; }
  .glyph.gold { color: var(--gold); }
  .glyph.sage { color: var(--sage); }
  .glyph.coral { color: var(--coral); }
  .title { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .state { font-size: var(--t-cap); font-weight: 600; text-transform: none; flex: none; }
  .state.gold { color: var(--gold); }
  .state.sage { color: var(--sage); }
  .state.coral { color: var(--coral); }
  .fact { color: var(--muted); font-size: var(--t-sm); white-space: nowrap; flex: none; overflow: hidden; text-overflow: ellipsis; }
  .btn {
    margin-left: auto;
    min-height: 28px;
    padding: 3px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-sm);
    flex: none;
  }
  /* The clock keeps the row short; the full stamp is in the title. */
  time { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; flex: none; }
  .detail {
    margin: 0;
    padding: 0 12px 8px;
    color: var(--muted);
    font-size: var(--t-sm);
    overflow-wrap: anywhere;
  }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); }
    /* One row on a phone: the glyph, the name, the state and the action. */
    time { display: none; }
    /* A folded row keeps its count and last time on the phone too; only the
       per-run duration is dropped, since a fold shows the last time instead. */
    .fact { font-size: var(--t-cap); }
    .btn { min-height: 36px; }
  }
</style>
