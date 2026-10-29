<script lang="ts">
  // The row for a low-disk machine event: one designed line in the feed, and
  // the same row in a session DM. It reads the typed payload of the post,
  // never its text, so it cannot disagree with what the probe found.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import { formatBytes } from '../settings/display.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineEvent(post))
  let name = $derived(payload?.name ?? 'A node')
  let free = $derived(payload ? formatBytes(payload.freeBytes) : '')
  let chips = $derived(payload ? [{ label: `${free} free`, tone: 'gold' }, { label: payload.block }] : [])
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M8 2.4 14.2 13H1.8Z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
    <path d="M8 6.4v3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    <circle cx="8" cy="11.2" r="0.8" fill="currentColor" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">Dyna starts no new work on {name} until space is freed.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Low disk on {name}" {chips} {icon} detail={body} />

<style>
  .text { margin: 0; }
</style>
