<script lang="ts">
  // The row for the engine's timed flush: no post woke the root seat for a
  // while, so the engine delivers the quiet posts a room held as one batch.
  // It is a quiet one-line row; it wakes root and names nobody else.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { engineEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(engineEvent(post))
  let title = $derived(payload ? `Root seat idle for ${payload.minutes} minutes` : 'Timed flush')
  let hasBody = $derived(!!payload?.detail)
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <circle cx="8" cy="8" r="5.6" fill="none" stroke="currentColor" stroke-width="1.4" />
    <path d="M8 5v3.4l2.2 1.4" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet body()}
  {#if payload?.detail}<p class="text">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} {title} summary="The held posts came as one batch" {icon} detail={hasBody ? body : undefined} />

{#if surface === 'dm' && !hasBody}<EventDetails text={post.text} />{/if}

<style>
  .text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
