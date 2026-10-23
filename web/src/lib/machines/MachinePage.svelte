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
  import SystemLoadCard from '../rooms/SystemLoadCard.svelte'
  import { pollLoad } from '../rooms/systemLoad.ts'
  import { ago } from '../ui/format.ts'
  import { api } from '../../api.ts'
  import type { SystemLoad } from '../api/system.ts'
  import type { MachineAgent, MachineDetail, MachinePatch, MachineRole } from '../api/machine.ts'
  import { ciRunDetail, ciRunText, computersText, count, diskText, loadText, memoryPct, reasonText, roleLabel, routeText, runtimeText, statusText, systemText, versionText } from './machines.ts'

  let { machine, error = '', busy = false, probing = false, onback, onagent, onsave, onprobe = () => {} }: {
    machine: MachineDetail | null
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

  // What the last probe read. A machine no probe has reached has no
  // capabilities, and the page then shows no figure at all.
  let caps = $derived(machine?.capabilities ?? null)
  let remote = $derived(machine && !machine.local ? machine.load ?? null : null)
  let memory = $derived(remote ? memoryPct(remote) : null)

  const ROLES: MachineRole[] = ['engine', 'agents', 'ci']

  function toggleRole(role: MachineRole) {
    if (!machine) return
    const roles = machine.roles.includes(role) ? machine.roles.filter((r) => r !== role) : [...machine.roles, role]
    onsave({ roles })
  }

  function setSlots(e: Event & { currentTarget: HTMLInputElement }) {
    const slots = Number(e.currentTarget.value)
    if (!Number.isInteger(slots) || slots < 0 || slots === machine?.ciSlots) return
    onsave({ ciSlots: slots })
  }

  function setSSH(e: Event & { currentTarget: HTMLInputElement }) {
    const ssh = e.currentTarget.value.trim()
    if (!machine || ssh === machine.ssh) return
    onsave({ ssh })
  }
</script>

<section class="machine">
  <header class="top">
    <button class="back" onclick={onback} title="Back to the machine list"><span aria-hidden="true">‹</span> Machines</button>
    {#if machine}<span class="status {machine.status}">{statusText(machine.status)}</span>{/if}
  </header>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  {#if !machine}
    <Skeleton label="Loading machine" />
  {:else}
    <div class="body">
      <Card title="Overview">
        <Row dense text="Status">
          {#snippet trail()}<span class="value">{statusText(machine.status)}</span>{/snippet}
        </Row>
        <Row dense text="Roles">
          {#snippet trail()}<span class="chips">{#each machine.roles as role (role)}<Chip>{roleLabel(role)}</Chip>{/each}</span>{/snippet}
        </Row>
        <Row dense text="Route">
          {#snippet trail()}<span class="value mono">{routeText(machine)}</span>{/snippet}
        </Row>
        {#if machine.status === 'offline'}
          <!-- r8: a designed line, never the engine's probe error text. -->
          <Row dense text="Last probe">
            {#snippet trail()}<span class="value warn">Did not reach the machine. Check the SSH route, then probe again.</span>{/snippet}
          </Row>
        {:else if machine.status === 'degraded'}
          <Row dense text="Needs attention">
            {#snippet meta()}<span class="gold">{reasonText(machine.statusReason)}</span>{/snippet}
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

      {#if machine.local}
        <div class="usage">
          {#if load}
            <SystemLoadCard title="Usage" {load} rows={false} />
          {:else}
            <Card title="Usage"><p class="note">Reading this machine…</p></Card>
          {/if}
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
        </div>
      {/if}

      {#if remote}
        <Card title="Usage">
          <Row dense text="CPU">
            {#snippet trail()}
              {#if remote.cpuPercent !== undefined}
                <span class="mem">
                  <span class="bar" aria-hidden="true"><span class="fill" style="width: {remote.cpuPercent}%"></span></span>
                  <span class="value">{remote.cpuPercent}%</span>
                </span>
              {:else}<span class="value">No reading</span>{/if}
            {/snippet}
          </Row>
          <Row dense text="Load average">
            {#snippet trail()}<span class="value mono">{remote.loadAvg.map((v) => v.toFixed(2)).join(' · ')}</span>{/snippet}
          </Row>
          <Row dense text="Memory">
            {#snippet trail()}
              {#if memory !== null}
                <span class="mem">
                  <span class="bar" aria-hidden="true"><span class="fill" style="width: {memory}%"></span></span>
                  <span class="value">{loadText(remote)}</span>
                </span>
              {:else}<span class="value">No reading</span>{/if}
            {/snippet}
          </Row>
        </Card>
      {/if}

      {#if machine.ci}
        {@const ci = machine.ci}
        {@const runs = machine.ciRuns ?? []}
        <Card title="CI" count={runs.length || null}>
          <Row dense text="Slots now">
            {#snippet trail()}<span class="value">{ci.used} of {ci.slots} used</span>{/snippet}
          </Row>
          {#each runs as run (`${run.url}@${run.started}`)}
            <Row dense text={ciRunText(run)}>
              {#snippet meta()}
                {#if run.trial}<Chip>Trial</Chip>{/if}
                <span>Started {ago(run.started)}</span>
                {#if ciRunDetail(run)}<span>{ciRunDetail(run)}</span>{/if}
              {/snippet}
              {#snippet trail()}
                <span class="value">{count(run.slots, 'slot')}</span>
                {#if run.url}<a class="open" href={run.url} target="_blank" rel="noreferrer" title="Open this PR on GitHub">Open ↗</a>{/if}
              {/snippet}
            </Row>
          {:else}
            <p class="note">No CI run here now.</p>
          {/each}
        </Card>
      {/if}

      <Card title="Configuration">
        <Row dense text="Enabled">
          {#snippet meta()}<span class="note">A disabled machine takes no new work.</span>{/snippet}
          {#snippet trail()}<Switch on={machine.enabled} label="Enabled" disabled={busy} onchange={(on: boolean) => onsave({ enabled: on })} />{/snippet}
        </Row>
        <Row dense text="Roles">
          {#snippet trail()}
            <span class="chips">
              {#each ROLES as role (role)}
                <button
                  class="role"
                  class:on={machine.roles.includes(role)}
                  aria-pressed={machine.roles.includes(role)}
                  disabled={busy || role === 'engine'}
                  title={role === 'engine' ? (machine.local ? 'This machine always runs the engine' : 'Only this engine\u2019s machine runs the engine') : undefined}
                  onclick={() => toggleRole(role)}
                >{roleLabel(role)}</button>
              {/each}
            </span>
          {/snippet}
        </Row>
        <Row dense text="CI slots">
          {#snippet trail()}<input class="num" type="number" min="0" step="1" aria-label="CI slots" value={machine.ciSlots} disabled={busy} onchange={setSlots} />{/snippet}
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
        <Row dense text="Probe now">
          {#snippet meta()}<span class="note">Reads what the machine has and how it is loaded.</span>{/snippet}
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
  .chips { display: inline-flex; flex-wrap: wrap; gap: var(--s1); justify-content: flex-end; }
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
  .role { flex: none; padding: 2px 9px; border-radius: 9px; background: var(--raise); color: var(--muted); font-size: var(--t-cap); font-weight: 600; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  .role.on { background: var(--iris-wash); color: var(--iris); box-shadow: inset 0 0 0 1px var(--iris-edge); }
  .role:disabled { opacity: 0.6; }
  .num { width: 72px; background: var(--field); border: 0; border-radius: var(--r-xs); padding: 4px 8px; color: var(--ink); font: inherit; font-size: var(--t-sm); }
  .ssh { width: 200px; max-width: 100%; background: var(--field); border: 0; border-radius: var(--r-xs); padding: 4px 8px; color: var(--ink); font-size: var(--t-sm); }
  .err { margin: 0; padding: var(--s2) var(--s4) 0; color: var(--coral); font-size: var(--t-sm); }
  @media (max-width: 760px) {
    .top { padding: var(--s2) var(--s3) 0; }
    .body { padding: var(--s2) var(--s3) var(--s3); }
    .back { min-height: 44px; }
    .role { min-height: 32px; }
    .probe { min-height: 32px; }
    .num, .ssh { min-height: 36px; }
  }
</style>
