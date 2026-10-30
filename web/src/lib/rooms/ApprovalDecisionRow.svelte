<script lang="ts">
  // The quiet row for a decision on an approval request: who chose what, and
  // the reason when they gave one. It reads the typed payload, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { approvalDecidedEvent } from './eventPayloads.ts'
  import type { ApprovalAction } from './events.gen.ts'
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
  >
    {#snippet icon()}<span aria-hidden="true">{decided.action === 'deny' ? '✕' : '✓'}</span>{/snippet}
    {#snippet extra()}<a href="#approval-{decided.requestId}">View request</a>{/snippet}
  </EventRow>
{/if}
