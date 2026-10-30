<script lang="ts">
  // The interval and time limit of a built-in service, and a reset to the
  // defaults. Each save writes through the call that owns it and the page then
  // reads the service again.
  import SettingRow from '../ui/SettingRow.svelte'

  interface Props {
    service: string
    everyMinutes: number
    timeout: string
    busy: boolean
    onWrite: (id: string, value: string | boolean) => Promise<void>
  }
  let { service, everyMinutes, timeout, busy, onWrite }: Props = $props()

  let every = $state('')
  let limit = $state('')
  $effect(() => { every = String(everyMinutes) })
  $effect(() => { limit = timeout })
</script>

<div class="schedule">
  <SettingRow title="Runs every" description="Minutes between runs.">
    {#snippet control()}
      <input type="number" min="1" bind:value={every} aria-label="Minutes between runs" />
      <button class="act" disabled={busy || Number(every) === everyMinutes} onclick={() => onWrite(`service.every:${service}`, every)}>Save</button>
    {/snippet}
  </SettingRow>
  <SettingRow title="Time limit" description="Longest one run may take, such as 5m0s.">
    {#snippet control()}
      <input type="text" bind:value={limit} aria-label="Time limit of one run" />
      <button class="act" disabled={busy || limit.trim() === timeout || !limit.trim()} onclick={() => onWrite(`service.timeout:${service}`, limit.trim())}>Save</button>
    {/snippet}
  </SettingRow>
  <SettingRow title="Defaults" description="Restores the switch, the interval and the time limit.">
    {#snippet control()}
      <button class="act" disabled={busy} onclick={() => onWrite(`service.reset:${service}`, true)}>Reset</button>
    {/snippet}
  </SettingRow>
</div>

<style>
  .schedule { margin: var(--s2) 0 var(--s2) 15px; border: 1px solid var(--line-soft); border-radius: var(--r-xs); }
  input { width: 7rem; background: var(--field); color: var(--ink-2); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 2px 6px; font: inherit; font-size: var(--t-sm); }
  .act { color: var(--muted); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 1px 8px; font-size: var(--t-sm); }
  .act:disabled { opacity: 0.6; }
</style>
