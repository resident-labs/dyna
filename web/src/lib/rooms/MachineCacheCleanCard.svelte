<script lang="ts">
  // One bounded cache-cleanup failure in the feed and in a session DM. The
  // typed payload names the path and reason; no runtime log reaches the card.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineCacheCleanFailedEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineCacheCleanFailedEvent(post))
  let name = $derived(payload?.name ?? 'A node')
  let path = $derived(payload?.path ?? 'its CI cache')
  let reason = $derived(payload?.reason ?? 'the cleanup did not finish')
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M3 4.5h8M6 4.5V3h4v1.5M4.5 4.5l.6 8h5.8l.3-3M12 9v3M12 14v.1" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">Dyna could not remove <code>{path}</code>: {reason}. It will try the cleanup again.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Cache cleanup failed on {name}" {icon} detail={body} />

<style>
  .text { margin: 0; }
  code { overflow-wrap: anywhere; }
</style>
