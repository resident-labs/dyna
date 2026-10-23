<script lang="ts">
  // The card for an alert: a doctor report or the engine stuck read-only.
  // Something needs the owner's seat now, so the card has a coral edge, the
  // source of the report and the next action. It reads the typed payload of
  // the post, never its text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { alertEvent } from './eventPayloads.ts'
  import type { AlertSource } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const sourceNames: Record<AlertSource, string> = { doctor: 'Doctor report', engine: 'Engine alert' }

  let payload = $derived(alertEvent(post))
  let title = $derived.by(() => {
    const p = payload
    if (!p) return 'Alert'
    const name = sourceNames[p.source] ?? 'Alert'
    return p.reportId ? `${name} ${p.reportId}` : name
  })
</script>

<div class="card alert {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">!</span>
    <span class="title">{title}</span>
    {#if payload?.trigger}<span class="fact">{payload.trigger}</span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  {#if payload?.summary}<p class="summary">{payload.summary}</p>{/if}
  {#if payload?.detail && payload.detail !== payload.summary}<pre class="detail">{payload.detail}</pre>{/if}
  {#if payload?.action}<p class="action">Next: <code>{payload.action}</code></p>{/if}
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
  .fact { color: var(--muted); font-size: var(--t-sm); }
  time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .summary { margin: 0; padding: 0 12px 8px; color: var(--ink-2); }
  .detail {
    margin: 0 12px 10px;
    padding: 8px;
    max-height: 240px;
    overflow: auto;
    background: var(--main);
    border-radius: var(--r-sm);
    color: var(--muted);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: var(--t-sm)/1.5 var(--mono);
  }
  .action { margin: 0; padding: 0 12px 10px; color: var(--muted); font-size: var(--t-sm); }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); }
    time { display: none; }
    .summary, .detail, .action { margin-inline: var(--s2); padding-inline: 0; }
  }
</style>
