<script lang="ts">
  // The card for a workflow run's result: its name, how it ended, how many
  // agent calls served it, and how long it took. The summary is the run's own
  // result line; the full output lives in the run view. It reads the typed
  // payload, never the post text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { runEvent } from './eventPayloads.ts'
  import type { RunState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import RunIcon from '../workflows/RunIcon.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<RunState, string> = { completed: 'done', failed: 'failed', stopped: 'stopped' }
  const stateTone: Record<RunState, string> = { completed: 'sage', failed: 'coral', stopped: 'gold' }

  let payload = $derived(runEvent(post))
  let state = $derived<RunState>(payload?.state ?? 'completed')
  let name = $derived(payload?.name || 'Workflow')
  // A run under a second reads in milliseconds, so a quick run does not look
  // like a clock that failed to move.
  let elapsed = $derived.by(() => {
    const ms = payload?.durationMs ?? 0
    if (ms <= 0) return ''
    if (ms < 1000) return `${ms} ms`
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
    return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
  })
  let counts = $derived.by(() => {
    const steps = payload?.steps ?? 0
    const agents = payload?.agents ?? 0
    if (!steps) return ''
    const call = steps === 1 ? '1 agent call' : `${steps} agent calls`
    return agents > 1 ? `${call} · ${agents} agents` : call
  })
</script>

<div class="card {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph {stateTone[state]}" aria-hidden="true"><RunIcon size={15} /></span>
    <span class="title">
      <span class="name">{name}</span>
      <span class="state {stateTone[state]}">{stateWords[state]}</span>
    </span>
    {#if counts}<span class="fact">{counts}</span>{/if}
    {#if elapsed}<span class="fact">{elapsed}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.summary}<p class="body">{payload.summary}</p>{/if}
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
  .sum { display: flex; align-items: baseline; gap: 8px; padding: 8px 12px; color: var(--ink-2); font-size: var(--t-sm); }
  .glyph { flex: none; display: grid; place-items: center; align-self: center; }
  .glyph.sage { color: var(--sage); }
  .glyph.coral { color: var(--coral); }
  .glyph.gold { color: var(--gold); }
  .title { display: flex; align-items: baseline; gap: 6px; flex: 1; min-width: 0; }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .state { flex: none; font-size: var(--t-cap); font-weight: 600; }
  .state.sage { color: var(--sage); }
  .state.coral { color: var(--coral); }
  .state.gold { color: var(--gold); }
  .fact { flex: none; color: var(--muted); font-size: var(--t-cap); white-space: nowrap; }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .body { margin: 0; padding: 0 12px 10px; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 44px; padding: 6px var(--s2); }
    time, .fact { display: none; }
  }
</style>
