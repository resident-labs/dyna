<script lang="ts">
  import Section from '../ui/Section.svelte'
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import StatusPill from '../ui/StatusPill.svelte'
  import Avatar from '../ui/Avatar.svelte'
  import type { MachineAgent, MachineCIRun, MachineWork, MachineWorkItem } from '../api/machine.ts'
  import { ago, duration } from '../ui/format.ts'
  import { ciRunDetail, ciRunText, memoryUseText, workHoldsText, workKindText, workLabelText, workWhoText } from './machines.ts'

  let { work, failed = false, agents = [], runs = [], onagent }: {
    work: MachineWork | null; failed?: boolean; agents?: MachineAgent[]; runs?: MachineCIRun[]; onagent?: (agent: MachineAgent) => void
  } = $props()
</script>

{#snippet item(unit: MachineWorkItem)}
  <Row dense text={workLabelText(unit)}>
    {#snippet lead()}<StatusPill label={workKindText(unit.kind)} />{/snippet}
    {#snippet meta()}
      {#if workWhoText(unit)}<span>{workWhoText(unit)}</span>{/if}
      <span>{workHoldsText(unit)}</span>
      {#if unit.heavy}<span>IO-heavy</span>{/if}
      {#each unit.resources ?? [] as name (name)}<span>{name}</span>{/each}
    {/snippet}
    {#snippet trail()}{#if unit.position === undefined}<span class="note">{duration(unit.since)}</span>{/if}{/snippet}
    {#snippet detail()}{#if unit.position !== undefined}<span class="wait">{unit.reason || 'Waiting for the next queue pass'}</span>{/if}{/snippet}
  </Row>
{/snippet}

<Section title="Work">
  <Card>
    {#if !work}<p class="note">{failed ? 'Could not read this node’s queue. It will try again on the next change.' : 'Reading the queue…'}</p>
    {:else}
      {@const c = work.capacity}
      {#if c.diskBlock}<p class="wait">Blocked: {c.diskBlock}. Dyna admits no new work here.</p>{/if}
      <p class="note">{memoryUseText(c)} · {c.heavyRunning} of {c.heavyLimit} IO-heavy</p>
      {#each c.resources ?? [] as resource (resource.name)}<p class="note">{resource.name}: {resource.used} of {resource.limit} in use</p>{/each}
      <h3>Running</h3>
      {#each agents as agent (agent.id)}
        <Row dense text={agent.name} onclick={() => onagent?.(agent)}>
          {#snippet lead()}<Avatar name={agent.name} hue={agent.hue} size={24} /><StatusPill label="Agent" />{/snippet}
          {#snippet meta()}{#if agent.room}<span>{agent.room}</span>{/if}{/snippet}
        </Row>
      {/each}
      {#each runs as run (`${run.url}@${run.started}`)}
        <Row dense text={ciRunText(run)}>
          {#snippet lead()}<StatusPill label="CI" />{/snippet}
          {#snippet meta()}<span>Started {ago(run.started)}</span>{#if ciRunDetail(run)}<span>{ciRunDetail(run)}</span>{/if}{/snippet}
          {#snippet trail()}{#if run.url}<a href={run.url} target="_blank" rel="noreferrer">PR ↗</a>{/if}{/snippet}
        </Row>
      {/each}
      {#each work.running ?? [] as unit (unit.id)}{@render item(unit)}{/each}
      {#if !agents.length && !runs.length && !work.running?.length}<p class="note">Nothing runs on this node.</p>{/if}
      <h3>Waiting</h3>
      {#each work.waiting ?? [] as unit (unit.id)}{@render item(unit)}{:else}<p class="note">No work waits.</p>{/each}
    {/if}
  </Card>
</Section>

<style>
  h3 { margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--muted); }
  .note { color: var(--faint); margin: 0; }
  .wait { color: var(--gold); font-size: var(--t-cap); }
  a { color: var(--iris); }
</style>
