<script lang="ts">
  // The Work section of the Machine page: what the node's queue counts against,
  // the jobs and checks that run on it, and the work that waits, in admission
  // order. The engine words every reason; this draws what it reports. A null
  // work is the first read, or a read that failed, each shown as a quiet line.
  import Card from '../ui/Card.svelte'
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import type { MachineWork, MachineWorkItem } from '../api/machine.ts'
  import { duration } from '../ui/format.ts'
  import { cpuUseText, diskLimitText, memoryUseText, workHoldsText, workKindText, workLabelText, workWhoText } from './machines.ts'

  let { work, failed = false }: { work: MachineWork | null; failed?: boolean } = $props()
</script>

{#snippet item(unit: MachineWorkItem)}
  <Row dense text={workLabelText(unit)}>
    {#snippet lead()}<Chip tone={unit.kind === 'job' ? 'iris' : ''} outline>{workKindText(unit.kind)}</Chip>{/snippet}
    {#snippet meta()}
      {#if workWhoText(unit)}<span>{workWhoText(unit)}</span>{/if}
      <span>{workHoldsText(unit)}</span>
      {#if unit.heavy}<Chip tone="gold" outline>IO-heavy</Chip>{/if}
      {#each unit.resources ?? [] as name (name)}<Chip outline>{name}</Chip>{/each}
    {/snippet}
    {#snippet trail()}
      {#if unit.kind === 'job'}<span class="id mono" title="Job ID">{unit.id}</span>{/if}
      {#if unit.source?.head}<span class="id mono" title="Commit">{unit.source.head}</span>{/if}
      {#if unit.position === undefined}<span class="note">{duration(unit.since)}</span>{/if}
    {/snippet}
    {#snippet detail()}
      {#if unit.position !== undefined}<span class="wait">{unit.reason || 'Waiting for the next queue pass'}</span>{/if}
    {/snippet}
  </Row>
{/snippet}

<Card title="Work" count={work ? work.running.length + work.waiting.length || null : null}>
  {#if !work}
    <p class="note">{failed ? 'Could not read this node\'s queue. It will try again on the next change.' : 'Reading the queue…'}</p>
  {:else}
    {@const c = work.capacity}
    <Row dense text="CPUs held">
      {#snippet trail()}<span class="value">{cpuUseText(c)}</span>{/snippet}
    </Row>
    <Row dense text="Memory held">
      {#snippet trail()}<span class="value">{memoryUseText(c)}</span>{/snippet}
    </Row>
    <Row dense text="Disk">
      {#snippet meta()}{#if c.diskBlock}<span class="warn">Blocked: {c.diskBlock}. Dyna admits no new work here.</span>{/if}{/snippet}
      {#snippet trail()}<span class="value">{diskLimitText(c)}</span>{/snippet}
    </Row>
    <Row dense text="IO-heavy running">
      {#snippet trail()}<span class="value">{c.heavyRunning} of {c.heavyLimit}</span>{/snippet}
    </Row>
    {#each c.resources ?? [] as resource (resource.name)}
      <Row dense text="Resource {resource.name}">
        {#snippet trail()}<span class="value">{resource.used} of {resource.limit} in use</span>{/snippet}
      </Row>
    {/each}

    <h5>Running <span class="n">{work.running.length}</span></h5>
    {#each work.running as unit (unit.id)}{@render item(unit)}{:else}<p class="note">Nothing runs on this node.</p>{/each}

    <h5>Waiting <span class="n">{work.waiting.length}</span></h5>
    {#each work.waiting as unit (unit.id)}{@render item(unit)}{:else}<p class="note">No work waits.</p>{/each}
  {/if}
</Card>

<style>
  h5 { margin: var(--s2) 0 0; font-size: var(--t-cap); font-weight: 600; color: var(--muted); }
  .n { color: var(--faint); font-weight: 500; }
  .value { color: var(--ink-2); }
  .note { color: var(--faint); margin: 0; }
  .warn { color: var(--coral); }
  .wait { color: var(--gold); font-size: var(--t-cap); }
  .id { color: var(--faint); max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mono { font-family: var(--mono); font-size: var(--t-sm); }
</style>
