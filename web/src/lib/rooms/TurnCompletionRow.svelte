<script lang="ts">
  // The row for the end of a session turn: how the turn ended, which session
  // ran it, and the report it gave. It reads the typed payload, never the post
  // text, and draws the same row in the room feed and in a session DM. The
  // report is markdown, shown as the body; a long one scrolls.
  //
  // A session DM keeps the agent's own text one click away: a turn end is
  // input that session's parent read, so EventDetails shows exactly that.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { turnEvent } from './eventPayloads.ts'
  import type { TurnState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import RunIcon from '../workflows/RunIcon.svelte'
  import { clock, markdown, short } from '../ui/format.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    room = undefined,
  }: { post: EventPost; notice?: EventNotice; surface?: EventSurface; room?: { name: string } } = $props()

  const stateWords: Record<TurnState, string> = { completed: 'completed', failed: 'failed', stopped: 'stopped' }
  const stateTone: Record<TurnState, string> = { completed: 'sage', failed: 'coral', stopped: 'gold' }

  let payload = $derived(turnEvent(post))
  let state = $derived<TurnState>(payload?.state ?? 'completed')
  // The room feed replaces a completion's stored summary with the turn's full
  // result; a session DM keeps the stored post.
  let body = $derived(post.fullResult && post.text ? post.text : payload?.summary ?? '')
  let error = $derived(payload?.error ?? '')
  let name = $derived(short(payload?.session ?? post.speaker ?? post.from ?? '', room?.name))
</script>

<div class="row {surface} {stateTone[state]}" data-id={post.id}>
  <div class="sum">
    <span class="glyph glyph-mark" aria-hidden="true">{state === 'failed' ? '✕' : state === 'stopped' ? '⊘' : '✓'}</span>
    <span class="title">
      <span class="name hue">{name}</span>
      <span class="what">turn {payload?.turn ?? post.turn ?? '?'}</span>
      <span class="state {stateTone[state]}">{stateWords[state]}</span>
    </span>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if body}
    <div class="body prose">{@html markdown(body)}</div>
  {:else if error}
    <p class="body error">{error}</p>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .row { margin: var(--s1) var(--s4); border-left: 2px solid var(--line); border-radius: 6px; background: var(--side); }
  .row.dm { margin: var(--s1) 0; }
  .row.sage { border-left-color: var(--sage); }
  .row.coral { border-left-color: var(--coral); }
  .row.gold { border-left-color: var(--gold); }
  .sum { display: flex; align-items: baseline; gap: 8px; padding: 8px 12px 0; color: var(--ink-2); font-size: var(--t-sm); }
  .glyph { flex: none; width: 1.1em; text-align: center; }
  .sage .glyph { color: var(--sage); }
  .coral .glyph { color: var(--coral); }
  .gold .glyph { color: var(--gold); }
  .title { display: flex; align-items: baseline; gap: 6px; flex: 1; min-width: 0; flex-wrap: wrap; }
  .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .what { color: var(--muted); }
  .state { flex: none; font-size: var(--t-cap); font-weight: 600; }
  .state.sage { color: var(--sage); }
  .state.coral { color: var(--coral); }
  .state.gold { color: var(--gold); }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .body { margin: 0; padding: var(--s1) 12px var(--s2) 34px; color: var(--ink-2); font-size: var(--t-sm); max-height: 320px; overflow: auto; overflow-wrap: anywhere; }
  .error { color: var(--coral); white-space: pre-wrap; }
  @media (max-width: 760px) {
    .row { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2) 0; }
    time { display: none; }
    .body { padding: var(--s1) var(--s2) var(--s2) var(--s2); }
  }
</style>
