<script lang="ts">
  // One quiet owner row for an instruction diff that reached several sessions.
  // The sessions' own agent notes remain in their DMs. This card reads the
  // typed batch and keeps the reached sessions in a compact details fold.
  import type { EventNotice, Room } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { instructionBatchEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock, short } from '../ui/format.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    room = undefined,
  }: { post: EventPost; notice?: EventNotice; surface?: EventSurface; room?: Room } = $props()

  let payload = $derived(instructionBatchEvent(post))
  let title = $derived.by(() => {
    const sections = payload?.sections ?? []
    if (sections.length === 1) return `${sections[0]?.name ?? 'Standing instructions'} changed`
    if (sections.length > 1) return `${sections.length} instruction sections changed`
    return 'Standing instructions changed'
  })
</script>

<div class="card {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">✦</span>
    <span class="line">{title}</span>
    {#if payload}<span class="reach">· reached {payload.sessions.length} {payload.sessions.length === 1 ? 'session' : 'sessions'}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload}
    <details class="sessions">
      <summary>Sessions reached</summary>
      <ul>
        {#each payload.sessions as session (session)}<li>{short(session, room?.name)}</li>{/each}
      </ul>
    </details>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .card { margin: 6px var(--s4); color: var(--muted); font-size: var(--t-sm); }
  .card.dm { margin: 6px 0; }
  .sum { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .glyph { flex: none; color: var(--iris); }
  .line { flex: 1; min-width: 0; overflow-wrap: anywhere; color: var(--ink-2); }
  .reach { flex: none; color: var(--muted); font-size: var(--t-cap); }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .sessions { margin: 4px 0 0 24px; }
  .sessions summary { cursor: pointer; color: var(--faint); font-size: var(--t-cap); }
  .sessions ul { margin: 4px 0 0; padding-left: 18px; color: var(--muted); }
  @media (max-width: 760px) {
    .card { margin: 6px var(--s2); }
    .sum { flex-wrap: wrap; gap: 4px 8px; }
    time { display: none; }
  }
</style>
