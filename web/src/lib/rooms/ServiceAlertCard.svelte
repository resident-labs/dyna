<script lang="ts">
  // The card for a failed service turn: one alert from the service to the
  // root seat, folded per turn. It names the service, the turn and the error,
  // and wakes root once. It reads the typed payload of the post.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { serviceFailedEvent } from './eventPayloads.ts'
  import type { ServiceRole } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const roleNames: Record<ServiceRole, string> = { librarian: 'Librarian', reviewer: 'Reviewer', helper: 'Helper' }

  let payload = $derived(serviceFailedEvent(post))
  let title = $derived(payload ? `${roleNames[payload.service] ?? 'Service'} failed` : 'Service failed')
  let model = $derived.by(() => [payload?.profile, payload?.model].filter(Boolean).join(' · '))
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  chips={[{ label: 'failed', tone: 'coral', failure: true }]}
  summary={[payload?.turn ? `turn ${payload.turn}` : '', model, payload?.error ?? '', payload?.session ?? ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}!{/snippet}
  {#snippet detail()}
    {#if payload?.error}<pre class="detail">{payload.error}</pre>{/if}
    {#if payload?.workerStderr}
      <p class="who">Worker stderr</p>
      <pre class="detail">{payload.workerStderr}</pre>
    {/if}
    {#if payload?.session}<p class="who">{payload.session}</p>{/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .detail {
    margin: 0 0 8px;
    padding: 8px;
    max-height: 200px;
    overflow: auto;
    background: var(--main);
    border-radius: var(--r-sm);
    color: var(--ink-2);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: var(--t-sm)/1.5 var(--mono);
  }
  .who { margin: 0; padding: 0; color: var(--faint); font-size: var(--t-cap); }
</style>
