<script lang="ts">
  // The quiet row for a decision on an approval request: who chose what, and
  // the reason when they gave one. It reads the typed payload, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { approvalDecidedEvent } from './eventPayloads.ts'
  import type { ApprovalAction } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const words: Record<ApprovalAction, string> = { approve: 'approved', deny: 'denied', pass: 'passed on', auto: 'approved by rule' }

  let decided = $derived(approvalDecidedEvent(post))
</script>

{#if decided}
  <EventRow
    id={post.id}
    ts={post.ts}
    {surface}
    title="{decided.actor} {words[decided.action]} a request"
    summary={[decided.action === 'pass' && decided.at ? `now with ${decided.at}` : '', decided.reason ?? ''].filter(Boolean).join(' · ')}
    chips={decided.action === 'deny' ? [{ label: 'denied', failure: true }] : []}
  >
    {#snippet icon()}<span aria-hidden="true">{decided.action === 'deny' ? '✕' : '✓'}</span>{/snippet}
  </EventRow>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/if}
