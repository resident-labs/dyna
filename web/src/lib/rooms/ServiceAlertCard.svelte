<script lang="ts">
  // The card for a failed service turn: one alert from the service to the
  // root seat, folded per turn. It names the service, the turn and the error,
  // and wakes root once. It reads the typed payload of the post.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { serviceFailedEvent } from './eventPayloads.ts'
  import type { ServiceRole } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const roleNames: Record<ServiceRole, string> = { librarian: 'Librarian', reviewer: 'Reviewer', helper: 'Helper' }

  let payload = $derived(serviceFailedEvent(post))
  let title = $derived(payload ? `${roleNames[payload.service] ?? 'Service'} failed` : 'Service failed')
  let model = $derived.by(() => [payload?.profile, payload?.model].filter(Boolean).join(' · '))
</script>

<div class="card alert {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">!</span>
    <span class="title">{title}</span>
    {#if payload?.turn}<span class="fact">turn {payload.turn}</span>{/if}
    {#if model}<span class="fact" title={model}>{model}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.error}<pre class="detail">{payload.error}</pre>{/if}
  {#if payload?.session}<p class="who">{payload.session}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    border-left: 3px solid var(--coral);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum { display: flex; align-items: center; gap: 8px; padding: 8px 12px; color: var(--ink-2); }
  .glyph {
    flex: none;
    width: 16px;
    height: 16px;
    line-height: 16px;
    text-align: center;
    border-radius: 50%;
    background: var(--coral);
    color: var(--main);
    font-size: var(--t-cap);
    font-weight: 700;
  }
  .title { font-weight: 600; flex: none; }
  .fact { color: var(--muted); font-size: var(--t-sm); max-width: 24ch; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .detail {
    margin: 0 12px 8px;
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
  .who { margin: 0; padding: 0 12px 10px; color: var(--faint); font-size: var(--t-cap); }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); flex-wrap: wrap; }
    time { display: none; }
    .detail { margin-inline: var(--s2); }
    .who { padding-inline: var(--s2); }
  }
</style>
