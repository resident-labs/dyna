<script lang="ts">
  // The row for a worktree-port capacity event. It reads the typed pool facts,
  // so the feed and a session DM show the same sizes and next action.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { worktreePortsEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(worktreePortsEvent(post))
  let used = $derived(payload ? `${payload.used.toLocaleString()} of ${payload.limit.toLocaleString()} blocks` : '')
  let remaining = $derived(payload ? payload.limit - payload.used : 0)
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M8 2.4 14.2 13H1.8Z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
    <path d="M8 6.4v3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    <circle cx="8" cy="11.2" r="0.8" fill="currentColor" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">Dyna gives each retained worktree a block of 10 ports. {remaining.toLocaleString()} {remaining === 1 ? 'block remains' : 'blocks remain'}. Remove unneeded worktrees with <code>dyna git worktrees gc</code> before new sessions cannot start.</p>
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Worktree port pool nears exhaustion" chips={payload ? [{ label: used, tone: 'gold' }, { label: `${payload.threshold}%` }] : []} {icon} detail={body} />

<style>
  .text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
