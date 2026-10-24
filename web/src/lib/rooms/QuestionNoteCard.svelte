<script lang="ts">
  // The quiet row for a withdrawn question. It belongs to the question's own
  // post, so it sits next to it and names who withdrew it and why, instead of
  // arriving as a message from the session that closed. It reads the typed
  // payload of the post.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { questionEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(questionEvent(post))
  let title = $derived(payload ? `Question ${payload.questionId} withdrawn` : 'Question withdrawn')
  let summary = $derived([payload?.by ? `by ${payload.by}` : '', payload?.reason ?? ''].filter(Boolean).join(' · '))
</script>

{#snippet icon()}
  <span aria-hidden="true">?</span>
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} {title} {summary} {icon} />

{#if surface === 'dm'}<EventDetails text={post.text} />{/if}
