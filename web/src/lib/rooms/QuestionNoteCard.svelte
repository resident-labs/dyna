<script lang="ts">
  // The quiet row for a withdrawn question. It belongs to the question's own
  // post, so it sits next to it and names who withdrew it and why, instead of
  // arriving as a message from the session that closed. It reads the typed
  // payload of the post.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { questionEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(questionEvent(post))
</script>

<div class="note" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">?</span>
    <span class="title">Question {payload?.questionId ?? ''} withdrawn{payload?.reason ? `: ${payload.reason}` : ''}</span>
    {#if payload?.by}<span class="fact">by {payload.by}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .note {
    margin: 4px var(--s4) 6px var(--gutter, 56px);
    padding: 4px 0;
    font-size: var(--t-sm);
    color: var(--faint);
  }
  .sum { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .glyph { flex: none; width: 16px; text-align: center; color: var(--faint); font-weight: 700; }
  .title { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fact { flex: none; }
  time { margin-left: auto; flex: none; font-size: var(--t-cap); white-space: nowrap; }
  @media (max-width: 760px) {
    .note { margin: 4px var(--s2); }
    .fact { display: none; }
  }
</style>
