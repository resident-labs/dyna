<script lang="ts">
  // The row for the engine's timed flush: no post woke the root seat for a
  // while, so the engine delivers the quiet posts a room held as one batch.
  // It is a quiet one-line row; it wakes root and names nobody else.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { engineEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(engineEvent(post))
  let title = $derived(payload ? `Root seat idle for ${payload.minutes} minutes` : 'Timed flush')
</script>

<div class="note" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">⏱</span>
    <span class="title">{title}</span>
    <span class="fact">the held posts came as one batch</span>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.detail}<p class="detail">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .note {
    margin: 6px var(--s4) 6px var(--gutter, 56px);
    padding: 6px 0;
    border-top: 1px solid var(--hair);
    font-size: var(--t-sm);
    color: var(--muted);
  }
  .sum { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .glyph { flex: none; width: 16px; text-align: center; color: var(--gold); }
  .title { flex: none; color: var(--ink-2); font-weight: 600; }
  .fact { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--faint); }
  time { margin-left: auto; flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .detail { margin: 4px 0 0 24px; white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .note { margin: 4px var(--s2); }
    .fact { display: none; }
  }
</style>
