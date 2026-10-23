<script lang="ts">
  // The card for a low-disk machine event: one designed line in the feed, and
  // the same card in a session DM. It reads the typed payload of the post,
  // never its text, so it cannot disagree with what the probe found.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { machineEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import { formatBytes } from '../settings/display.ts'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(machineEvent(post))
  let name = $derived(payload?.name ?? 'A machine')
  let free = $derived(payload ? formatBytes(payload.freeBytes) : '')
  let floor = $derived(payload ? formatBytes(payload.floorBytes) : '')
</script>

<div class="card {surface}" data-id={post.id}>
  <div class="sum">
    <span class="glyph" aria-hidden="true">⚠</span>
    <span class="title"><span class="what">Low disk on</span><span class="name">{name}</span></span>
    {#if payload}<span class="fact">free <b>{free}</b></span><span class="fact">needs <b>{floor}</b></span>{/if}
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </div>
  <div class="body">
    <p class="reason">Dyna starts no CI work on {name} until space is freed.</p>
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    font-size: 13px;
  }
  .body { padding: 0 12px 10px; }
  .glyph { color: var(--gold, #b8860b); }
  .title { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
  .what { color: var(--muted); }
  .name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fact { color: var(--muted); font-size: 12px; }
  .fact b { color: var(--text); font-weight: 600; }
  time { margin-left: auto; color: var(--muted); font-size: 12px; }
  .reason { margin: 0; color: var(--text); font-size: 13px; }
</style>
