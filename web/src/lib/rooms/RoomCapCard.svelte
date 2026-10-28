<script lang="ts">
  // The row for a room-cap machine event: one room's work is close to a cap
  // of its resource slice. It reads the typed payload of the post, never its
  // text, so the sizes cannot disagree with what the doctor read.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { roomCapEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import { formatBytes } from '../settings/display.ts'
  import type { RoomCapKind } from './events.gen.ts'

  // What each cap does when the room reaches it, in words a person reads.
  const capName: Record<RoomCapKind, string> = { 'memory.high': 'memory', 'memory.max': 'memory', 'pids.max': 'process' }
  const capEffect: Record<RoomCapKind, string> = {
    'memory.high': 'the kernel starts to reclaim and slow the room down',
    'memory.max': 'the OOM killer will kill a process in the room',
    'pids.max': 'new processes in the room will fail to start',
  }

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(roomCapEvent(post))
  let room = $derived(payload?.room ?? 'This room')
  let capWords = $derived(payload ? capName[payload.cap] : '')
  let used = $derived(payload ? (payload.cap === 'pids.max' ? `${payload.current.toLocaleString()} of ${payload.limit.toLocaleString()} processes` : `${formatBytes(payload.current)} of ${formatBytes(payload.limit)}`) : '')
  let share = $derived(payload && payload.limit > 0 ? `${Math.round((100 * payload.current) / payload.limit)}%` : '')
  let effect = $derived(payload ? capEffect[payload.cap] : '')
</script>

{#snippet icon()}
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M8 1.6c3.1 0 5.4 2 5.4 4.7 0 2.4-1.7 3.6-2.8 4.8-.5.5-.8 1.1-.9 1.9H6.3c-.1-.8-.4-1.4-.9-1.9C4.3 9.9 2.6 8.7 2.6 6.3 2.6 3.6 4.9 1.6 8 1.6z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" />
    <path d="M6.4 13.8h3.2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" />
  </svg>
{/snippet}

{#snippet body()}
  <p class="text">{room} uses {used}: {effect}. Finish or pause the room's heaviest work; the programs below are where it goes.</p>
  {#if payload?.topByMemory?.length}
    <p class="list-title">Biggest by memory</p>
    <ul class="trees">
      {#each payload.topByMemory as t (t.pid)}
        <li>{t.command} (pid {t.pid}) · {formatBytes(t.memory)} · {t.count} {t.count === 1 ? 'process' : 'processes'}</li>
      {/each}
    </ul>
  {/if}
  {#if payload?.topByCount?.length}
    <p class="list-title">Most processes</p>
    <ul class="trees">
      {#each payload.topByCount as t (t.pid)}
        <li>{t.command} (pid {t.pid}) · {t.count} {t.count === 1 ? 'process' : 'processes'} · {formatBytes(t.memory)}</li>
      {/each}
    </ul>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title="Room {room} nears its {capWords} cap" chips={payload ? [{ label: used, tone: 'gold' }, { label: share }] : []} {icon} detail={body} />

<style>
  .text { margin: 0 0 4px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .list-title { margin: 6px 0 2px; font-weight: 600; }
  .trees { margin: 0; padding-left: 18px; }
  .trees li { margin: 1px 0; }
</style>
