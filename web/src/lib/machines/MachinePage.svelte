<script lang="ts">
  import Card from '../ui/Card.svelte'
  import Section from '../ui/Section.svelte'
  import SettingRow from '../ui/SettingRow.svelte'
  import StatusPill from '../ui/StatusPill.svelte'
  import Switch from '../ui/Switch.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import ScopeList from './ScopeList.svelte'
  import MachineWorkCard from './MachineWorkCard.svelte'
  import NodeUsageCard from './NodeUsageCard.svelte'
  import { pollLoad } from '../system/systemLoad.ts'
  import { ago, roomLabel } from '../ui/format.ts'
  import { api } from '../../api.ts'
  import type { Room } from '../api/room.ts'
  import type { SystemLoad } from '../api/system.ts'
  import type { MachineAgent, MachineDetail, MachinePatch, MachineWork, NodeHistoryPoint, NodeDiagnosis } from '../api/machine.ts'
  import { routeText, runtimeText, statusText, versionText } from './machines.ts'

  let { machine, rooms = [], owner = true, error = '', busy = false, probing = false, onback, onagent, onsave, onprobe = () => {} }: {
    machine: MachineDetail | null; rooms?: Pick<Room, 'id' | 'name' | 'displayName'>[]; owner?: boolean
    error?: string; busy?: boolean; probing?: boolean; onback: () => void
    onagent?: (agent: MachineAgent) => void; onsave: (patch: MachinePatch) => void; onprobe?: () => void
  } = $props()

  let load = $state<SystemLoad | null>(null)
  $effect(() => {
    load = null
    if (!machine?.local) return
    return pollLoad(() => api.systemLoad(), (value) => { load = value })
  })
  let workRead = $state<{ id: string; value: MachineWork | null } | null>(null)
  let request = 0
  let workHere = $derived(workRead?.id === machine?.id ? workRead : null)
  $effect(() => {
    const id = machine?.id
    if (!id) return
    const n = ++request
    api.machineWork(id).then((value) => { if (n === request) workRead = { id, value } }, () => { if (n === request) workRead = { id, value: null } })
    return () => { request++ }
  })
  let historyRead = $state<{ id: string; points: NodeHistoryPoint[]; diagnosis: NodeDiagnosis | null } | null>(null)
  let historyRequest = 0
  let historyHere = $derived(historyRead?.id === machine?.id ? historyRead : null)
  $effect(() => {
    const id = machine?.id
    if (!id) return
    const n = ++historyRequest
    Promise.all([api.nodeHistory(id), api.nodeLastDiagnosis(id)]).then(([points, diagnosis]) => {
      if (n === historyRequest) historyRead = { id, points, diagnosis }
    }, () => { if (n === historyRequest) historyRead = { id, points: [], diagnosis: null } })
    return () => { historyRequest++ }
  })
  let caps = $derived(machine?.capabilities ?? null)
  let roomOptions = $derived(rooms.map((r) => ({ value: r.id, label: roomLabel(r) })).sort((a, b) => a.label.localeCompare(b.label)))
  function setLimit(field: 'maxIOHeavy' | 'maxDiskPercent', max: number) {
    return (e: Event & { currentTarget: HTMLInputElement }) => {
      const value = Number(e.currentTarget.value)
      if (!Number.isInteger(value) || value < 0 || value > max || value === machine?.[field]) return
      onsave({ [field]: value })
    }
  }
  const setIOHeavy = $derived(setLimit('maxIOHeavy', 1024))
  const setDiskPercent = $derived(setLimit('maxDiskPercent', 100))
  function setSSH(e: Event & { currentTarget: HTMLInputElement }) {
    const ssh = e.currentTarget.value.trim()
    if (machine && ssh !== machine.ssh) onsave({ ssh })
  }
  function setConnectTimeout(e: Event & { currentTarget: HTMLInputElement }) {
    const seconds = Number(e.currentTarget.value)
    if (Number.isInteger(seconds) && seconds >= 0 && seconds !== machine?.sshConnectTimeoutSeconds) onsave({ sshConnectTimeoutSeconds: seconds })
  }
</script>

<section class="machine">
  <header class="top"><button class="back" onclick={onback} title="Back to the node list">‹ Nodes</button></header>
  {#if error}<p class="err" role="alert">Could not update this node. Try again.</p>{/if}
  {#if !machine}<Skeleton label="Loading node" />
  {:else}
    <div class="body">
      <div class="facts">
        <strong>{machine.name}</strong>
        <StatusPill label={statusText(machine.status)} variant="dot" tone={machine.status === 'offline' ? 'danger' : 'neutral'} />
        <span>{routeText(machine)}</span>
        {#if caps}<span>{caps.os} {caps.arch}</span><span>{caps.cpus} CPUs</span>{#if caps.gpu}<span>GPU</span>{/if}<span>{runtimeText(caps.containers)}</span>{/if}
        {#if machine.dynaVersion}<span>Dyna {versionText(machine.dynaVersion)}</span>{/if}
      </div>
      {#if machine.status === 'offline'}<p class="err">Did not reach the node. Check the SSH route, then probe again.</p>{/if}
      <NodeUsageCard {machine} {load} work={workHere?.value ?? null} history={historyHere?.points ?? []} />
      <MachineWorkCard work={workHere?.value ?? null} failed={workHere !== null && workHere.value === null} agents={machine.local ? machine.running ?? [] : []} runs={machine.ciRuns ?? []} {onagent} />
      {#if machine.local || historyHere?.diagnosis}
        <Card title="Last diagnosis">
          {#snippet action()}{#if historyHere?.diagnosis}<span class="note">{ago(historyHere.diagnosis.at)}</span>{/if}{/snippet}
          {#if historyHere?.diagnosis}
            <p>{historyHere.diagnosis.summary}</p>
            {#each historyHere.diagnosis.findings as finding}<p>{finding.cause}: {finding.evidence}</p>{/each}
          {:else}<p class="note">No diagnosis yet.</p>{/if}
        </Card>
      {/if}
      <Section title="Settings"><Card flush divided>
        <SettingRow title="Takes work" description="A disabled node takes no new work.">
          {#snippet control()}<Switch on={machine.enabled} label="Takes work" disabled={busy} onchange={(on: boolean) => onsave({ enabled: on })} />{/snippet}
        </SettingRow>
        <SettingRow title="Rooms that can use it" description="Rooms that can use this node for jobs and local CI. None chosen means every room.">
          <ScopeList label="Rooms" values={machine.rooms ?? []} options={roomOptions} everyText="Every room" readOnly={!owner} {busy} onchange={(list: string[]) => onsave({ rooms: list })} />
        </SettingRow>
        <SettingRow title="Labels" description="The ci label makes this node run local CI checks.">
          <div class="labels">{#each machine.labels ?? [] as label}<StatusPill {label} />{/each}{#each machine.probedLabels ?? [] as label}<StatusPill label={label} value="probed" />{/each}{#if !machine.labels?.length && !machine.probedLabels?.length}<span class="note">None</span>{/if}</div>
        </SettingRow>
        <SettingRow title="Host checks" description="Allow only repositories you trust to run outside containers.">
          {#snippet control()}<Switch on={machine.allowHostChecks} label="Allow host checks" disabled={busy} onchange={(on: boolean) => onsave({ allowHostChecks: on })} />{/snippet}
          {#if machine.allowHostChecks && machine.hostChecksAllowedBy?.length}<span class="note">Allowed by {machine.hostChecksAllowedBy.join(' and ')}.</span>{/if}
        </SettingRow>
        <SettingRow title="Probe now" description="Reads what the node has and how it is loaded.">
          {#snippet control()}<button class="probe" disabled={busy || probing || !machine.enabled} onclick={onprobe}>{probing ? 'Probing…' : 'Probe'}</button>{/snippet}
        </SettingRow>
        <details><summary>Limits and connection</summary>
          <SettingRow title="IO-heavy limit" description="0 keeps the default: one per 16 CPUs, at least 1.">{#snippet control()}<input class="limit-input" type="number" min="0" step="1" aria-label="IO-heavy limit" value={machine.maxIOHeavy ?? 0} disabled={busy} onchange={setIOHeavy} />{/snippet}</SettingRow>
          <SettingRow title="Disk limit percent" description="0 keeps the default disk floor. A set percent is the used share above which the node admits no new work.">{#snippet control()}<input class="limit-input" type="number" min="0" max="100" step="1" aria-label="Disk limit percent" value={machine.maxDiskPercent ?? 0} disabled={busy} onchange={setDiskPercent} />{/snippet}</SettingRow>
          <SettingRow title="SSH route">{#snippet control()}{#if machine.local}<span>{routeText(machine)}</span>{:else}<input class="ssh" type="text" aria-label="SSH route" placeholder="user@host" value={machine.ssh} disabled={busy} onchange={setSSH} />{/if}{/snippet}</SettingRow>
          {#if !machine.local}<SettingRow title="SSH connect timeout" description="Seconds to wait for the connection.">{#snippet control()}<input class="limit-input" type="number" min="1" step="1" aria-label="SSH connect timeout in seconds" value={machine.sshConnectTimeoutSeconds ?? 10} disabled={busy} onchange={setConnectTimeout} />{/snippet}</SettingRow>{/if}
        </details>
      </Card></Section>
    </div>
  {/if}
</section>

<style>
  .machine { flex: 1; overflow-y: auto; display: flex; flex-direction: column; min-height: 0; }
  .top { padding: var(--s2) var(--s4) 0; }
  .back { color: var(--muted); padding: var(--s1) var(--s2); }
  .body { padding: var(--s3) var(--s4) var(--s4); display: flex; flex-direction: column; gap: var(--s3); max-width: 900px; }
  .facts, .labels { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); color: var(--muted); font-size: var(--t-sm); }
  .facts strong { color: var(--ink); font-size: var(--t-title); }
  .note { color: var(--muted); margin: 0; }
  .err { margin: 0; color: var(--coral); font-size: var(--t-sm); }
  .probe { padding: var(--s1) var(--s3); border-radius: var(--r-sm); background: var(--raise); color: var(--ink-2); }
  .probe:disabled { opacity: .6; }
  .limit-input, .ssh { background: var(--field); border: 0; border-radius: var(--r-xs); padding: var(--s1) var(--s2); color: var(--ink); font: inherit; }
  .limit-input { width: 72px; } .ssh { width: 200px; max-width: 100%; }
  summary { padding: var(--s3) var(--s4); cursor: pointer; color: var(--muted); }
</style>
