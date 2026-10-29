<script lang="ts">
  // The row for a scratch-freed machine event: one compact icon row in the feed,
  // and the same row in a session DM. It reads the typed payload of the post,
  // never its text, so it cannot disagree with what the sweep removed.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineScratchEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineScratchEvent(post))
  let name = $derived(payload?.name ?? 'A node')
  let chips = $derived(
    payload
      ? [
          { label: `${payload.folders} idle ${payload.folders === 1 ? 'folder' : 'folders'}` },
          { label: `${payload.builds} build ${payload.builds === 1 ? 'output' : 'outputs'}` },
        ]
      : [],
  )
  let freed = $derived(payload ? `${(payload.freedBytes / 2 ** 30).toFixed(1)} GiB` : 'Space')
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M3 4.5h10M6 4.5V3h4v1.5M4.5 4.5l.6 8h5.8l.6-8" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">{freed} freed from Dyna's scratch folder. Folders idle for 7 days went first, then idle build outputs while the node was low on disk.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Scratch freed on {name}" {chips} {icon} detail={body} />

<style>
  .text { margin: 0; }
</style>
