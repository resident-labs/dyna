<script lang="ts">
  // A pinned service is waiting. The card names the pin and why it cannot
  // run. The service does not start on another profile.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { serviceWaitEvent } from './eventPayloads.ts'
  import type { ServiceRole } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const roleNames: Record<ServiceRole, string> = { librarian: 'Librarian', reviewer: 'Reviewer', helper: 'Helper' }

  let payload = $derived(serviceWaitEvent(post))
  let title = $derived(payload ? `${roleNames[payload.service] ?? 'Service'} is waiting` : 'Service is waiting')
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  chips={[{ label: 'waiting', tone: 'gold' }]}
  summary={[payload?.profile ?? '', payload?.reason ?? ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}!{/snippet}
  {#snippet detail()}
    {#if payload?.reason}<p class="why">{payload.profile}: {payload.reason}</p>{/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .why { margin: 0; color: var(--ink-2); font-size: var(--t-sm); }
</style>
