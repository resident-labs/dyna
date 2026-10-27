<script lang="ts">
  // The row for a terminal request: a session asks the owner to run a command,
  // or the request ended. It reads the typed payload of the post, never the
  // text. The owner's terminal controls come with the terminal bar.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { terminalDoneEvent, terminalRequestEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let request = $derived(terminalRequestEvent(post))
  let done = $derived(terminalDoneEvent(post))
  let title = $derived(done ? `Terminal request ${done.requestId} ${done.verdict}` : `Terminal request ${request?.requestId ?? ''}`.trim())
  let summary = $derived(done ? [done.exit ? `exit ${done.exit.code}` : '', done.note ?? ''].filter(Boolean).join(' · ') : [request?.argv?.join(' ') ?? '', request?.reason ?? ''].filter(Boolean).join(' · '))
</script>

{#snippet icon()}
  <span aria-hidden="true">&gt;_</span>
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} {title} {summary} {icon} />

{#if surface === 'dm'}<EventDetails text={post.text} />{/if}
