<script lang="ts">
  // The interval and time limit of a built-in service, and a reset to the
  // defaults. Both read and take a time the way the settings do, such as 10m,
  // 1h or 1d, and save on Enter or blur like every other field here.
  import SettingRow from '../ui/SettingRow.svelte'
  import SettingControl from './SettingControl.svelte'
  import { settingDisplay } from '../settings/display.ts'
  import { minutes, minutesText } from './model.ts'
  import type { RegistryService, ServicePatch } from '../api/service.ts'
  import type { SettingItem } from '../api/settings.ts'

  interface Props {
    schedule: NonNullable<RegistryService['schedule']>
    busy: boolean
    onsave: (patch: ServicePatch) => Promise<void>
    onreset: () => Promise<void>
  }
  let { schedule, busy, onsave, onreset }: Props = $props()

  let errors = $state<Record<string, string>>({})
  const field = (key: string, display: string): SettingItem => ({ key, value: display, display, kind: 'duration', source: 'file', liveVsRestart: '', help: '' })

  function save(key: string, text: string, patch: (n: number) => ServicePatch): Promise<void> {
    const n = minutes(text)
    errors = { ...errors, [key]: n ? '' : 'Use a time such as 10m, 1h or 1d' }
    return n ? onsave(patch(n)) : Promise.resolve()
  }
</script>

<SettingRow title="Runs every" description="Time between runs">
  {#snippet control()}<SettingControl item={field('every', minutesText(schedule.everyMinutes))} label="Time between runs" onsave={(v: string) => save('every', v, (n) => ({ everyMinutes: n }))} />{/snippet}
  {#snippet status()}{#if errors.every}<span class="err" role="alert">{errors.every}</span>{/if}{/snippet}
</SettingRow>
<SettingRow title="Time limit" description="Longest one run may take">
  {#snippet control()}<SettingControl item={field('limit', settingDisplay({ kind: 'duration', value: schedule.timeout }))} label="Time limit of one run" onsave={(v: string) => save('limit', v, (n) => ({ timeout: `${n}m` }))} />{/snippet}
  {#snippet status()}{#if errors.limit}<span class="err" role="alert">{errors.limit}</span>{/if}{/snippet}
</SettingRow>
<SettingRow title="Defaults" description="Restores the switch, the interval and the time limit">
  {#snippet control()}<button class="btn" disabled={busy} onclick={onreset}>Reset</button>{/snippet}
</SettingRow>

<style>
  .err { color: var(--tone-danger-fg); }
</style>
