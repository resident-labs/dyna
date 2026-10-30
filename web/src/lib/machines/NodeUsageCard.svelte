<script lang="ts">
  import Card from '../ui/Card.svelte'
  import StatTile from '../ui/StatTile.svelte'
  import type { MachineDetail, MachineWork } from '../api/machine.ts'
  import type { SystemLoad } from '../api/system.ts'
  import { loadView } from '../system/systemLoad.ts'
  import { memoryPct, sizeText } from './machines.ts'
  import { diskTone, freshness, usageTone } from './nodeUsage.ts'

  let { machine, load, work }: { machine: MachineDetail; load: SystemLoad | null; work: MachineWork | null } = $props()
  let remote = $derived(machine.load)
  let localView = $derived(load ? loadView(load) : null)
  let cpu = $derived(machine.local ? load?.cpuPercent ?? null : remote?.cpuPercent ?? null)
  let total = $derived(machine.local ? load?.memory.total : remote?.memTotal)
  let used = $derived(machine.local ? load?.memory.used : remote ? remote.memTotal - Math.min(remote.memAvailable, remote.memTotal) : undefined)
  let memory = $derived(machine.local ? total && used !== undefined ? used / total * 100 : null : remote ? memoryPct(remote) : null)
  let average = $derived(machine.local ? load?.load[0] : remote?.loadAvg[0])
  let cpus = $derived(machine.local ? load?.cpus : machine.capabilities?.cpus)
  let disk = $derived(machine.capabilities?.freeDisk)
  let floor = $derived(machine.capabilities?.diskFloor)
  let capacity = $derived(work?.capacity)
</script>

<Card title="Usage">
  {#snippet action()}<span class="fresh">{freshness(machine)}</span>{/snippet}
  <div class="tiles">
    <StatTile label="CPU" value={cpu == null ? '–' : `${Math.round(cpu)}%`} detail={average === undefined || !cpus ? 'No reading' : `load ${average.toFixed(1)} on ${cpus} CPUs`} tone={usageTone(cpu)} />
    <StatTile label="Memory" value={memory == null ? '–' : `${Math.round(memory)}%`} detail={used === undefined || !total ? 'No reading' : `${sizeText(used)} of ${sizeText(total)}`} tone={usageTone(memory)} />
    <StatTile label="Disk free" value={disk === undefined ? '–' : sizeText(disk)} detail={floor === undefined ? 'No reading' : `floor ${sizeText(floor)}`} tone={diskTone(disk, floor)} />
    <StatTile label="Held by work" value={capacity?.cpus ? `${capacity.cpusUsed}/${capacity.cpus}` : '–'} detail="CPUs reserved" />
  </div>
  <!-- The node history chart belongs here. -->
  {#if localView}
    {#if localView.known}
      <div class="bar" role="img" aria-label="CPU split">
        {#each localView.split as s (s.key)}<span class="seg {s.key}" style="width: {s.pct}%"></span>{/each}
      </div>
      <div class="legend">{#each localView.split as s (s.key)}<span><i class="dot {s.key}"></i>{s.label} {s.text}</span>{/each}</div>
    {:else}<p class="note">Dyna's share is not available on this system.</p>{/if}
    <div class="bar" role="img" aria-label="Memory">
      {#each localView.mem as s (s.key)}<span class="seg m-{s.key}" style="width: {s.pct}%"></span>{/each}
    </div>
    <div class="legend">{#each localView.mem as s (s.key)}<span><i class="dot m-{s.key}"></i>{s.label} {s.text}</span>{/each}</div>
  {/if}
</Card>

<style>
  .fresh, .detail { color: var(--faint); font-size: var(--t-cap); }
  .tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); border: 1px solid var(--hair); border-radius: var(--r-sm); overflow: hidden; }
  .bar { display: flex; height: 8px; border-radius: 4px; overflow: hidden; background: var(--raise); }
  .seg { display: block; height: 100%; }
  .legend { display: flex; flex-wrap: wrap; gap: var(--s1) var(--s3); font-size: var(--t-cap); color: var(--muted); }
  .legend span { display: inline-flex; align-items: center; gap: 5px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
  .engine { background: var(--iris); } .agents { background: var(--hue-3); } .computers { background: var(--hue-2); }
  .other, .m-other { background: var(--faint); } .m-dyna { background: var(--hue-4); }
  .m-free { background: transparent; } .dot.m-free { box-shadow: inset 0 0 0 1px var(--faint); }
  .note { margin: 0; color: var(--faint); font-size: var(--t-sm); }
  @media (max-width: 650px) { .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); } :global(.tiles > :nth-child(2)) { border-right: 0; } :global(.tiles > :nth-child(-n+2)) { border-bottom: 1px solid var(--hair); } }
</style>
