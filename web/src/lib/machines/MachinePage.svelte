<script lang="ts">
  // The Machine page: what one machine is, what it uses now, what its last
  // probe read and how it is configured. This engine's own machine reports
  // live use from the load sampler; a remote machine shows the load its last
  // probe read. A machine that no probe has reached shows "not probed"
  // instead of empty figures.
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import Switch from '../ui/Switch.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import ScopeList from './ScopeList.svelte'
  import MachineWorkCard from './MachineWorkCard.svelte'
  import type { Room } from '../api/room.ts'
  import NodeUsageCard from './NodeUsageCard.svelte'
  import { pollLoad } from '../system/systemLoad.ts'
  import { ago, roomLabel } from '../ui/format.ts'
  import { api } from '../../api.ts'
  import type { SystemLoad } from '../api/system.ts'
  import type { MachineAgent, MachineDetail, MachinePatch, MachineWork } from '../api/machine.ts'
  import { ciRunDetail, ciRunText, computersText, diskText, routeText, runtimeText, statusText, systemText, versionText } from './machines.ts'

  let { machine, rooms = [], owner = true, error = '', busy = false, probing = false, onback, onagent, onsave, onprobe = () => {} }: {
    machine: MachineDetail | null
    // rooms are the rooms a CI machine can be limited to. owner is whether the
    // caller is the owner: only the owner changes the room and suite lists, so
    // anyone else sees them read-only.
    rooms?: Pick<Room, 'id' | 'name' | 'displayName'>[]
    owner?: boolean
    error?: string
    busy?: boolean
    probing?: boolean
    onback: () => void
    onagent?: (agent: MachineAgent) => void
    onsave: (patch: MachinePatch) => void
    onprobe?: () => void
  } = $props()

  // Live use reads from the engine's load sampler, the same call Home's card
  // uses. A remote machine has no sampler on this engine, so it shows none.
  let load = $state<SystemLoad | null>(null)
  $effect(() => {
    load = null
    if (!machine?.local) return
    return pollLoad(() => api.systemLoad(), (value) => { load = value })
  })

  // The Work section reads again whenever the page's machine answers anew: the
  // engine's machines event and the page's own read both replace it, so no
  // timer is added here. A slow answer never replaces a newer one, and an
  // answer for a machine that closed is dropped.
  let workRead = $state<{ id: string; value: MachineWork | null } | null>(null)
  let workRequest = 0
  let workHere = $derived(workRead && workRead.id === machine?.id ? workRead : null)
  $effect(() => {
    const id = machine?.id
    if (!id) return
    const request = ++workRequest
    api.machineWork(id).then((value) => { if (request === workRequest) workRead = { id, value } }, () => { if (request === workRequest) workRead = { id, value: null } })
  })

  // What the last probe read. A machine no probe has reached has no
  // capabilities, and the page then shows no figure at all.
  let caps = $derived(machine?.capabilities ?? null)

  let roomOptions = $derived(rooms.map((r) => ({ value: r.id, label: roomLabel(r) })).sort((a, b) => a.label.localeCompare(b.label)))

  function setLimit(field: 'maxIOHeavy' | 'maxDiskPercent', max: number) {
    return (e: Event & { currentTarget: HTMLInputElement }) => {
      const value = Number(e.currentTarget.value)
      if (!Number.isInteger(value) || value < 0 || value > max) return
      if (machine && value === machine[field]) return
      onsave({ [field]: value })
    }
  }
  const setIOHeavy = $derived(setLimit('maxIOHeavy', 1024))
  const setDiskPercent = $derived(setLimit('maxDiskPercent', 100))

  function setSSH(e: Event & { currentTarget: HTMLInputElement }) {
    const ssh = e.currentTarget.value.trim()
    if (!machine || ssh === machine.ssh) return
    onsave({ ssh })
  }

  function setConnectTimeout(e: Event & { currentTarget: HTMLInputElement }) {
    const seconds = Number(e.currentTarget.value)
    if (!Number.isInteger(seconds) || seconds < 0 || seconds === machine?.sshConnectTimeoutSeconds) return
    onsave({ sshConnectTimeoutSeconds: seconds })
  }
</script>

<section class="machine">
  <header class="top">
    <button class="back" onclick={onback} title="Back to the node list"><span aria-hidden="true">‹</span> Nodes</button>
    {#if machine}<span class="status {machine.status}">{statusText(machine.status)}</span>{/if}
  </header>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  {#if !machine}
    <Skeleton label="Loading node" />
  {:else}
    <div class="body">
      <Card title="Overview">
        <Row dense text="Status">
          {#snippet trail()}<span class="value">{statusText(machine.status)}</span>{/snippet}
        </Row>
        <Row dense text="Route">
          {#snippet trail()}<span class="value mono">{routeText(machine)}</span>{/snippet}
        </Row>
        {#if machine.status === 'offline'}
          <!-- r8: a designed line, never the engine's probe error text. -->
          <Row dense text="Last probe">
            {#snippet trail()}<span class="value warn">Did not reach the node. Check the SSH route, then probe again.</span>{/snippet}
          </Row>
        {/if}
        {#if machine.seenAt}
          <Row dense text="Last seen">
            {#snippet trail()}<span class="value">{ago(machine.seenAt)}</span>{/snippet}
          </Row>
        {/if}
        {#if caps}
          <Row dense text="System">
            {#snippet trail()}<span class="value">{systemText(caps)}</span>{/snippet}
          </Row>
          <Row dense text="GPU">
            {#snippet trail()}<span class="value">{caps.gpu ? 'Yes' : 'No'}</span>{/snippet}
          </Row>
          <Row dense text="Containers">
            {#snippet trail()}<span class="value">{runtimeText(caps.containers)}</span>{/snippet}
          </Row>
          <Row dense text="Computers">
            {#snippet trail()}<span class="value">{computersText(caps)}</span>{/snippet}
          </Row>
          {#if caps.freeDisk !== undefined}
            <Row dense text="Disk">
              {#snippet trail()}<span class="value">{diskText(caps)}</span>{/snippet}
            </Row>
          {/if}
        {/if}
        {#if machine.dynaVersion}
          {@const version = machine.dynaVersion}
          <Row dense text="Dyna">
            {#snippet trail()}<span class="value mono" title={version}>{versionText(version)}</span>{/snippet}
          </Row>
        {/if}
        {#if machine.harnesses?.length}
          {@const harnesses = machine.harnesses ?? []}
          <Row dense text="Harnesses">
            {#snippet trail()}<span class="value">{harnesses.map((h) => `${h.name} ${h.version}`).join(' · ')}</span>{/snippet}
          </Row>
        {/if}
      </Card>

      <NodeUsageCard {machine} {load} work={workHere?.value ?? null} />
      {#if machine.local}
          <Card title="Running now" count={machine.running?.length || null}>
            {#each machine.running ?? [] as agent (agent.id)}
              <Row dense text={agent.name} onclick={() => onagent?.(agent)}>
                {#snippet lead()}<Avatar name={agent.name} hue={agent.hue} size={24} />{/snippet}
                {#snippet trail()}{#if agent.room}<Chip>{agent.room}</Chip>{/if}{/snippet}
              </Row>
            {:else}
              <p class="note">No agent runs here now.</p>
            {/each}
          </Card>
      {/if}

      <MachineWorkCard work={workHere?.value ?? null} failed={workHere !== null && workHere.value === null} />

      <!-- The Work card holds the queue; this card keeps what it lacks: the PR each CI run checks. -->
      {#if machine.ciRuns?.length}
        <Card title="CI runs" count={machine.ciRuns.length}>
          {#each machine.ciRuns as run (`${run.url}@${run.started}`)}
            <Row dense text={ciRunText(run)}>
              {#snippet meta()}
                {#if run.trial}<Chip>Trial</Chip>{/if}
                <span>Started {ago(run.started)}</span>
                {#if ciRunDetail(run)}<span>{ciRunDetail(run)}</span>{/if}
              {/snippet}
              {#snippet trail()}
                {#if run.url}<a class="open" href={run.url} target="_blank" rel="noreferrer" title="Open this PR on GitHub">Open ↗</a>{/if}
              {/snippet}
            </Row>
          {/each}
        </Card>
      {/if}

      <Card title="Configuration">
        <Row dense text="Enabled">
          {#snippet meta()}<span class="note">A disabled node takes no new work.</span>{/snippet}
          {#snippet trail()}<Switch on={machine.enabled} label="Enabled" disabled={busy} onchange={(on: boolean) => onsave({ enabled: on })} />{/snippet}
        </Row>
        <Row dense text="IO-heavy limit">
          {#snippet meta()}<span class="note">0 keeps the default: one per 16 CPUs, at least 1.</span>{/snippet}
          {#snippet trail()}<input class="num" type="number" min="0" step="1" aria-label="IO-heavy limit" value={machine.maxIOHeavy ?? 0} disabled={busy} onchange={setIOHeavy} />{/snippet}
        </Row>
        <Row dense text="Disk limit %">
          {#snippet meta()}<span class="note">0 keeps the default: at least 20 GiB and 5% of the disk stay free, but never more than 10% of it. A set percent is the used share above which the node admits no new work.</span>{/snippet}
          {#snippet trail()}<input class="num" type="number" min="0" max="100" step="1" aria-label="Disk limit percent" value={machine.maxDiskPercent ?? 0} disabled={busy} onchange={setDiskPercent} />{/snippet}
        </Row>
        <Row dense text="Rooms">
          {#snippet meta()}
            <span class="note">Local CI runs only for these rooms. None chosen means every room.</span>
            {#if !owner}<span class="note">Only the owner can change this.</span>{/if}
          {/snippet}
          {#snippet detail()}<ScopeList label="Rooms" values={machine.rooms ?? []} options={roomOptions} everyText="Every room" readOnly={!owner} {busy} onchange={(list: string[]) => onsave({ rooms: list })} />{/snippet}
        </Row>
        <Row dense text="Owner labels">
          {#snippet meta()}
            <span class="note">The ci label makes this node run local CI checks. dyna node edit {machine.name} --label ci sets it.</span>
          {/snippet}
          {#snippet detail()}<span class="note">{machine.labels?.length ? machine.labels.join(', ') : 'none'}</span>{/snippet}
        </Row>
        <Row dense text="Probed labels">
          {#snippet meta()}<span class="note">Read from the last successful probe.</span>{/snippet}
          {#snippet detail()}<span class="note">{machine.probedLabels?.length ? machine.probedLabels.join(', ') : 'none'}</span>{/snippet}
        </Row>
        <Row dense text="Host checks">
          {#snippet meta()}
            <span class="note">Off by default. Allow only repositories you trust to run outside containers.</span>
            {#if machine.allowHostChecks && machine.hostChecksAllowedBy?.length}<span class="note">Allowed by {machine.hostChecksAllowedBy.join(' and ')}.</span>{/if}
          {/snippet}
          {#snippet trail()}<Switch on={machine.allowHostChecks} label="Allow host checks" disabled={busy} onchange={(on: boolean) => onsave({ allowHostChecks: on })} />{/snippet}
        </Row>
        <Row dense text="SSH route">
          {#snippet trail()}
            {#if machine.local}
              <span class="value">{routeText(machine)}</span>
            {:else}
              <input class="ssh mono" type="text" aria-label="SSH route" placeholder="user@host" value={machine.ssh} disabled={busy} onchange={setSSH} />
            {/if}
          {/snippet}
        </Row>
        {#if !machine.local}
          <Row dense text="SSH connect timeout">
            {#snippet meta()}<span class="note">Seconds to wait for the connection. Raise it for a node behind a slow jump host.</span>{/snippet}
            {#snippet trail()}<input class="num" type="number" min="1" step="1" aria-label="SSH connect timeout in seconds" value={machine.sshConnectTimeoutSeconds ?? 10} disabled={busy} onchange={setConnectTimeout} />{/snippet}
          </Row>
        {/if}
        <Row dense text="Probe now">
          {#snippet meta()}<span class="note">Reads what the node has and how it is loaded.</span>{/snippet}
          {#snippet trail()}
            <button class="probe" disabled={busy || probing || !machine.enabled} onclick={onprobe}>{probing ? 'Probing…' : 'Probe'}</button>
          {/snippet}
        </Row>
      </Card>
    </div>
  {/if}
</section>

<style>
  .machine { flex: 1; overflow-y: auto; display: flex; flex-direction: column; min-height: 0; }
  .top { display: flex; align-items: center; gap: var(--s3); padding: var(--s2) var(--s4) 0; min-width: 0; }
  .back { flex: none; display: inline-flex; align-items: center; gap: 2px; font-size: var(--t-sm); color: var(--faint); padding: var(--s1) var(--s2); border-radius: var(--r-xs); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .back:hover { color: var(--ink); background: var(--hover); }
  }
  .back:active { background: var(--select); }
  .status { margin-left: auto; flex: none; font-size: var(--t-sm); color: var(--faint); }
  .status.online { color: var(--sage); }
  .status.degraded { color: var(--gold); }
  .status.offline { color: var(--coral); }
  .status.disabled { color: var(--muted); }
  .body { padding: var(--s2) var(--s4) var(--s4); display: grid; gap: var(--s3); align-content: start; max-width: 900px; }
  .usage { display: grid; gap: var(--s3); }
  .value { color: var(--ink-2); }
  .note { color: var(--faint); }
  .open { color: var(--iris); text-decoration: none; }
  @media (hover: hover) { .open:hover { text-decoration: underline; } }
  .warn { color: var(--coral); }
  .gold { color: var(--gold); }
  .mono { font-family: var(--mono); font-size: var(--t-sm); }
  .mem { display: inline-flex; align-items: center; gap: var(--s2); }
  .bar { display: block; width: 72px; height: 4px; border-radius: 2px; overflow: hidden; background: var(--raise); }
  .fill { display: block; height: 100%; background: var(--iris); }
  .probe { flex: none; padding: 3px 11px; border-radius: 9px; background: var(--raise); color: var(--ink-2); font-size: var(--t-cap); font-weight: 600; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .probe:hover { background: var(--hover); color: var(--ink); }
  }
  .probe:active { background: var(--select); }
  .probe:disabled { opacity: 0.6; }
  .num { width: 72px; background: var(--field); border: 0; border-radius: var(--r-xs); padding: 4px 8px; color: var(--ink); font: inherit; font-size: var(--t-sm); }
  .ssh { width: 200px; max-width: 100%; background: var(--field); border: 0; border-radius: var(--r-xs); padding: 4px 8px; color: var(--ink); font-size: var(--t-sm); }
  .err { margin: 0; padding: var(--s2) var(--s4) 0; color: var(--coral); font-size: var(--t-sm); }
  @media (max-width: 760px) {
    .top { padding: var(--s2) var(--s3) 0; }
    .body { padding: var(--s2) var(--s3) var(--s3); }
    .back { min-height: 44px; }
    .probe { min-height: 32px; }
    .num, .ssh { min-height: 36px; }
  }
</style>
