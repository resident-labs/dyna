<script lang="ts">
  // The row for a cache-repair machine event: one compact icon row in the feed,
  // and the same row in a session DM. It reads the typed payload of the post,
  // never its text, so it cannot disagree with what the run repaired.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineCacheEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineCacheEvent(post))
  let name = $derived(payload?.name ?? 'A node')
  let caches = $derived(payload ? payload.caches.join(' and ') : 'its caches')
  let chips = $derived(payload ? payload.caches.map((label) => ({ label })) : [])
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M12.8 5.9A5 5 0 1 0 13 9.7" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
    <path d="M10.5 5.9h2.3V3.6" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">Dyna switched {caches} back to directories it manages. The old links are kept as backups.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Caches repaired on {name}" {chips} {icon} detail={body} />

<style>
  .text { margin: 0; }
</style>
