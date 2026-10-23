<script lang="ts">
  // The Machine page: what one machine is, what it uses now and how it is
  // configured. Only this engine's machine reports live use in this version;
  // a remote machine shows its stored settings and "not probed" until the
  // probe reads it.
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import Switch from '../ui/Switch.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import SystemLoadCard from '../rooms/SystemLoadCard.svelte'
  import { pollLoad } from '../rooms/systemLoad.ts'
  import { api } from '../../api.ts'
  import type { SystemLoad } from '../api/system.ts'
  import type { MachineAgent, MachineDetail, MachinePatch, MachineRole } from '../api/machine.ts'
  import { roleLabel, routeText, statusText } from './machines.ts'

  let { machine, error = '', busy = false, onback, onagent, onsave }: {
    machine: MachineDetail | null
    error?: string
    busy?: boolean
    onback: () => void
    onagent?: (agent: MachineAgent) => void
    onsave: (patch: MachinePatch) => void
  } = $props()

  // Live use reads from the engine's load sampler, the same call Home's card
  // uses. A remote machine has no sampler on this engine, so it shows none.
  let load = $state<SystemLoad | null>(null)
  $effect(() => {
    load = null
    if (!machine?.local) return
    return pollLoad(() => api.systemLoad(), (value) => { load = value })
  })

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
        {#if machine.dynaVersion}
          <Row dense text="Dyna">
            {#snippet trail()}<span class="value mono">{machine.dynaVersion}</span>{/snippet}
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

      {#if machine.ci}
        {@const ci = machine.ci}
        <Card title="CI">
          <Row dense text="Slots now">
            {#snippet trail()}<span class="value">{ci.used} of {ci.slots} used</span>{/snippet}
          </Row>
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
  .status.offline { color: var(--coral); }
  .status.disabled { color: var(--muted); }
  .body { padding: var(--s2) var(--s4) var(--s4); display: grid; gap: var(--s3); align-content: start; max-width: 900px; }
  .usage { display: grid; gap: var(--s3); }
  .value { color: var(--ink-2); }
  .chips { display: inline-flex; flex-wrap: wrap; gap: var(--s1); justify-content: flex-end; }
  .note { color: var(--faint); }
  .mono { font-family: var(--mono); font-size: var(--t-sm); }
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
    .num, .ssh { min-height: 36px; }
  }
</style>
