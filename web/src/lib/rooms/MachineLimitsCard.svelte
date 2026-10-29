<script lang="ts">
  // The row for a limits-off machine event: this machine cannot give each room
  // its own resource slice, so the room shares the agent slice with every
  // other room and has no process cap. It reads the typed payload of the post,
  // never its text, so the reason cannot disagree with what the engine found.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineLimitsEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineLimitsEvent(post))
  let reason = $derived(payload?.reason ?? 'this node lacks the delegated cgroup controllers')
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M8 2.2 13 4.4v3c0 2.9-2 5.4-5 6.4-3-1-5-3.5-5-6.4v-3L8 2.2z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
    <path d="M5.9 5.9l4.2 4.2M10.1 5.9 5.9 10.1" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">{reason}. Every room on this node shares one agent slice, and nothing caps a room's process count. Linux with systemd and the memory and pids controllers delegated to the user turns room limits on.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Room limits are off" summary={reason} {icon} detail={body} />

<style>
  .text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
