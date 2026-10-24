<script lang="ts">
  // The row for the owner's answer to a call request: they joined the call,
  // they declined it, or it was withdrawn before they answered. It reads the
  // typed payload of the post, never its text, and draws the same row in the
  // feed and in a session DM, which keeps the agent text behind Details. The
  // request's own ID is not a fact the owner reads here: it is the row's
  // tooltip, and the DM's Details keeps it in the agent text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { CallState } from './events.gen.ts'
  import { callEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import CallIcon from '../ui/CallIcon.svelte'

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
  let chips = $derived([{ label: state, tone: tones[state] }])
</script>

{#snippet icon()}
  <CallIcon size={15} />
{/snippet}

<!-- The wrapper carries the request ID as its tooltip: the row itself names the
     answer, and the ID stays out of the words the owner reads. -->
<div class="call" title={call ? `Call request ${call}` : undefined}>
  <EventRow id={post.id} ts={post.ts} {surface} title="Call request" summary={headlines[state]} {chips} {icon} />
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>
