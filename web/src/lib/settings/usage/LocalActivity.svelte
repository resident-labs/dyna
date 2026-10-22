<script lang="ts">
  import { errorText } from '../../errorText.ts'
  import { onMount, onDestroy } from 'svelte'
  import Loading from '../Loading.svelte'
  import { api } from '../../../api.js'
  import ActivityReport from './ActivityReport.svelte'
  import { rangeBounds, type ActivityRollup, type Period } from './activity.ts'
  import { requestGate } from './state.ts'
  import type { Room } from '../../api/room.ts'
  let { roomId = '' }: { roomId?: string } = $props()
  const PERIODS: [Period, string][] = [['today', 'Today'], ['7d', '7 days'], ['30d', '30 days'], ['90d', '90 days']]
  let scope = $state('')
  let period = $state<Period>('30d')
  let rooms = $state<Room[]>([])
  let snapshot = $state<ActivityRollup | null>(null)
  let loading = $state(false)
  let error = $state('')
  let mounted = false
  const gate = requestGate()
  onMount(() => { mounted = true; scope = roomId; api.rooms().then((value: Room[] | null) => { if (mounted) rooms = value ?? [] }).catch(() => {}); load() })
  onDestroy(() => { mounted = false; gate.invalidate() })
  async function load() {
    const ticket = gate.next()
    loading = true; error = ''
    const { from, to } = rangeBounds(period)
    try {
      const next = await api.usageRollup(scope, from, to)
      if (mounted && gate.current(ticket)) snapshot = next
    } catch (e) { if (mounted && gate.current(ticket)) { snapshot = null; error = errorText(e) } }
    finally { if (mounted && gate.current(ticket)) loading = false }
  }
  export function reload() { return load() }
  function choose(value: Period) { period = value; load() }
</script>
<section aria-busy={loading}>
  <div class="tools">
    <div class="seg" role="group" aria-label="Time range">{#each PERIODS as [value, label] (value)}<button aria-pressed={period === value} class:on={period === value} onclick={() => choose(value)}>{label}</button>{/each}</div>
    <label>Room<select bind:value={scope} onchange={load}><option value="">All rooms on this machine</option>{#each rooms as room (room.id)}<option value={room.id}>{room.name}</option>{/each}</select></label>
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if snapshot}<ActivityReport {snapshot} />{:else if loading}<Loading>Loading activity…</Loading>{/if}
</section>
<style>
  .tools { display: flex; align-items: end; flex-wrap: wrap; gap: var(--s3); }
  label { display: flex; flex-direction: column; gap: 7px; font-size: var(--t-sm); color: var(--muted); min-width: 140px; }
  select { font: inherit; font-size: var(--t-sm); color: var(--ink-2); background: var(--raise); border: 1px solid var(--line); border-radius: 6px; padding: var(--s2) 10px; min-height: 36px; max-width: 100%; }
  .seg { display: flex; background: var(--raise); border-radius: 6px; padding: 2px; }
  .seg button { padding: 6px 12px; border-radius: 5px; color: var(--muted); font-size: var(--t-sm); min-height: 32px; transition: background-color var(--motion-fast) var(--ease); }
  .seg button.on { background: var(--select); color: var(--ink); }
  .error { color: var(--coral); font-size: var(--t-sm); overflow-wrap: anywhere; }
  @media (max-width: 580px) { .seg { width: 100%; } .seg button { flex: 1; padding: 6px 4px; } label { flex: 1; } }
  @media (pointer: coarse) { select { font-size: var(--t-title); } select, .seg button, .btn { min-height: 44px; } }
</style>
