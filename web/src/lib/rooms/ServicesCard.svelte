<script lang="ts">
  // The Home Services card: every service and machine hook with its state and
  // last result. Failing rows come first. Healthy rows fold into one line that
  // opens on a click. A hook row opens its last run's log in the card; a
  // service row opens its room's Services page.
  import { onMount } from 'svelte'
  import { api } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import { ago } from '../ui/format.ts'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import type { Room } from '../api/room.ts'
  import { groupRows, healthyLine, hookRows, readLog, serviceRows, type ServiceCardRow } from './servicesCard.ts'

  let { rooms = [], onroom }: { rooms?: Room[]; onroom: (id: string, view?: string) => void } = $props()

  let rows = $state<ServiceCardRow[]>([])
  let loaded = $state(false)
  let error = $state('')
  let folded = $state(true) // the healthy line is closed
  let open = $state('') // id of the hook row whose log shows
  let logs = $state<Record<string, string>>({}) // row id -> log text

  let groups = $derived(groupRows(rows))
  const live = (room: Room) => !room.archived

  // A part that fails to load leaves the other parts on screen.
  async function load() {
    try {
      const hooks = hookRows((await api.globalHookSummary()) ?? [])
      const active = rooms.filter(live)
      const services = await Promise.all(active.map(async (room) => ({ room: room.id, services: (await api.listServices(room.id).catch(() => null)) ?? [] })))
      rows = [...serviceRows(services), ...hooks]
      error = ''
    } catch (e) {
      error = errorText(e)
    }
    loaded = true
  }

  async function show(row: ServiceCardRow) {
    const log = row.log
    if (!log) return
    if (log.kind === 'room') return onroom(log.room, 'services')
    open = open === row.id ? '' : row.id
    if (!open || logs[row.id]) return
    logs[row.id] = await readLog(api.globalHookRunLog, log)
  }

  // The first read, and a read again when the room list changes.
  $effect(() => { void rooms.length; load() })
  onMount(() => {
    const timer = setInterval(load, 60 * 1000)
    return () => clearInterval(timer)
  })
</script>

{#snippet line(row: ServiceCardRow)}
  <Row dense text={row.name} onclick={row.log ? () => show(row) : null}>
    {#snippet lead()}<span class="dot {row.tone}" aria-hidden="true"></span>{/snippet}
    {#snippet meta()}<span class:err={row.tone === 'bad'}>{row.state}</span>{#if row.at}<span>{ago(row.at)}</span>{/if}{/snippet}
    {#snippet trail()}{#if row.log}<span class="go">{row.log.kind === 'hook' ? (open === row.id ? 'Hide log' : 'Log') : 'Open'}</span>{/if}{/snippet}
  </Row>
  {#if open === row.id}<pre class="log mono">{logs[row.id] ?? 'Loading…'}</pre>{/if}
{/snippet}

{#if rows.length || error}
  <Card title="Services" count={rows.length}>
    <div class="scroll">
      {#each groups.failing as row (row.id)}{@render line(row)}{/each}
      {#if groups.healthy.length}
        <Row dense text={healthyLine(groups.healthy.length, groups.failing.length)} onclick={() => (folded = !folded)}>
          {#snippet lead()}<span class="dot ok" aria-hidden="true"></span>{/snippet}
          {#snippet trail()}<span class="go" aria-hidden="true">{folded ? '▸' : '▾'}</span>{/snippet}
        </Row>
        {#if !folded}{#each groups.healthy as row (row.id)}{@render line(row)}{/each}{/if}
      {/if}
      {#if error}<p class="err small" role="alert">Could not read services: {error}</p>{/if}
    </div>
  </Card>
{/if}

<style>
  /* A long list scrolls inside the card, so the card never pushes the rest of Home away. */
  .scroll { max-height: 320px; overflow-y: auto; }
  .dot { display: block; width: 8px; height: 8px; border-radius: 50%; background: var(--faint); }
  .dot.ok { background: var(--sage); }
  .dot.bad { background: var(--coral); }
  .err { color: var(--coral); }
  .small { font-size: var(--t-sm); margin: var(--s2); }
  .go { color: var(--faint); font-size: var(--t-sm); flex: none; }
  .log { margin: 0 var(--s2) var(--s2); padding: var(--s2) var(--s3); background: var(--rail); border-radius: 6px; max-height: 220px; overflow: auto; white-space: pre-wrap; word-break: break-all; color: var(--muted); font-size: var(--t-sm); }
</style>
