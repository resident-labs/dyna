<script lang="ts">
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { isGitHubWriteRefusedEvent } from './events.gen.ts'
  import EventRow from './EventRow.svelte'

  let { post, surface = 'feed' }: { post: EventPost; surface?: EventSurface } = $props()
  let event = $derived(isGitHubWriteRefusedEvent(post.payload) ? post.payload : undefined)
  let summary = $derived(event ? `${event.requester} cannot run ${event.verb} on ${event.repository}.` : 'A GitHub write was refused.')
</script>

{#snippet body()}
  {#if event}
    <p>Reason: {event.reason}. Policy rule: {event.rule}.</p>
    <p>Choose an allowed repository or ask the room owner to change the policy.</p>
  {/if}
{/snippet}
<EventRow id={post.id} ts={post.ts} {surface} title="GitHub write refused" {summary} detail={body} />
