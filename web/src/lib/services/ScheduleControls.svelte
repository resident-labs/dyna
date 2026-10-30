<script lang="ts">
  // The interval and time limit of a built-in service, and a reset to the
  // defaults. Each save writes through the call that owns it and the page then
  // reads the service again.
  import SettingRow from '../ui/SettingRow.svelte'
  import type { RegistryService, ServicePatch } from '../api/service.ts'

  interface Props {
    schedule: NonNullable<RegistryService['schedule']>
    busy: boolean
    onsave: (patch: ServicePatch) => Promise<void>
    onreset: () => Promise<void>
  }
  let { schedule, busy, onsave, onreset }: Props = $props()

  let every = $state('')
  let limit = $state('')
  $effect(() => { every = String(schedule.everyMinutes) })
  $effect(() => { limit = schedule.timeout })
</script>

<SettingRow title="Runs every" description="Minutes between runs.">
  {#snippet control()}
    <input class="field" type="number" min="1" bind:value={every} aria-label="Minutes between runs" />
    <button class="btn" disabled={busy || Number(every) === schedule.everyMinutes} onclick={() => onsave({ everyMinutes: Number(every) })}>Save</button>
  {/snippet}
</SettingRow>
<SettingRow title="Time limit" description="Longest one run may take, such as 5m0s.">
  {#snippet control()}
    <input class="field" type="text" bind:value={limit} aria-label="Time limit of one run" />
    <button class="btn" disabled={busy || limit.trim() === schedule.timeout || !limit.trim()} onclick={() => onsave({ timeout: limit.trim() })}>Save</button>
  {/snippet}
</SettingRow>
<SettingRow title="Defaults" description="Restores the switch, the interval and the time limit.">
  {#snippet control()}
    <button class="btn" disabled={busy} onclick={onreset}>Reset</button>
  {/snippet}
</SettingRow>

<style>
  .field { width: 6rem; min-height: 30px; padding: 0 var(--s2); background: var(--rail); border: 1px solid var(--border-soft); border-radius: var(--r-xs); color: var(--ink); font-size: var(--t-sm); }
  .field:focus { border-color: var(--iris-edge); outline: none; }
</style>
