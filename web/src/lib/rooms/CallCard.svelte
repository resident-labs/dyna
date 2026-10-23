<script lang="ts">
  // The row for the owner's answer to a call request: they joined the call,
  // they declined it, or it was withdrawn before they answered. It reads the
  // typed payload of the post, never its text, and draws the same row in the
  // feed and in a session DM, which keeps the agent text behind Details.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { CallState } from './events.gen.ts'
  import { callEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import CallIcon from '../ui/CallIcon.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const headlines: Record<CallState, string> = {
    joined: 'Owner joined the call',
    declined: 'Owner declined the call',
    withdrawn: 'Call request withdrawn',
  }
  const tones: Record<CallState, string> = { joined: 'sage', declined: 'coral', withdrawn: '' }

  let payload = $derived(callEvent(post))
  let state = $derived<CallState>(payload?.type ?? 'withdrawn')
  let call = $derived(payload?.call ?? '')
</script>

<div class="call {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph {tones[state]}"><CallIcon size={15} /></span>
    <span class="title">{headlines[state]}</span>
    {#if call}<span class="id mono" title="Call request {call}">{call}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .call {
    margin: 6px var(--s4) 6px var(--gutter, 56px);
    padding: 6px 0;
    border-top: 1px solid var(--hair);
    font-size: var(--t-sm);
    color: var(--muted);
  }
  .sum { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .call.dm { margin: 4px 0; }
  .call.dm .sum { flex-wrap: nowrap; }
  .glyph { flex: none; display: inline-flex; width: 16px; justify-content: center; color: var(--muted); }
  .glyph.sage { color: var(--sage); }
  .glyph.coral { color: var(--coral); }
  .title { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
  .id { flex: none; color: var(--faint); font-size: var(--t-cap); }
  time { margin-left: auto; flex: none; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  @media (max-width: 760px) {
    .call { margin: 4px var(--s2); }
  }
</style>
