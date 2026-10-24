<script lang="ts">
  // A compact warning for an automatic helper outcome. The helper, turn and
  // reason come from the typed payload; the engine's agent text stays behind
  // Details in a session DM.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { helperOutcomeEvent } from './eventPayloads.ts'
  import type { HelperOutcomeReason } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock, short } from '../ui/format.ts'

  let {
    post,
    notice = post.notice,
    surface = 'feed',
    room = undefined,
  }: { post: EventPost; notice?: EventNotice; surface?: EventSurface; room?: { name: string } } = $props()

  const reasonNames: Record<HelperOutcomeReason, string> = {
    'parent-unavailable': 'parent unavailable',
    'no-artifact': 'no artifact',
  }

  let payload = $derived(helperOutcomeEvent(post))
  let reason = $derived<HelperOutcomeReason>(payload?.reason ?? 'no-artifact')
  let name = $derived(short(payload?.session ?? post.speaker ?? '', room?.name))
</script>

<div class="helper-outcome {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph {reason === 'parent-unavailable' ? 'coral' : 'gold'}" aria-hidden="true">!</span>
    <span class="name hue">{name}</span>
    <span class="what">turn {payload?.turn ?? post.turn ?? '?'} finished</span>
    <span class="reason {reason === 'parent-unavailable' ? 'coral' : 'gold'}">{reasonNames[reason]}</span>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .helper-outcome {
    margin: 6px var(--s4) 6px var(--gutter, 56px);
    padding: 6px 0;
    border-top: 1px solid var(--hair);
    color: var(--muted);
    font-size: var(--t-sm);
  }
  .helper-outcome.dm { margin: 4px 0; }
  .sum { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .glyph { flex: none; width: 16px; text-align: center; font-weight: 700; }
  .coral { color: var(--coral); }
  .gold { color: var(--gold); }
  .name { flex: none; max-width: 32ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-weight: 600; }
  .what { flex: none; color: var(--ink-2); }
  .reason { flex: none; font-size: var(--t-cap); font-weight: 600; }
  time { margin-left: auto; flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  @media (max-width: 760px) {
    .helper-outcome { margin: 4px var(--s2); }
    .sum { flex-wrap: wrap; gap: 4px 8px; }
    time { display: none; }
  }
</style>
