<script lang="ts">
  // The interval and time limit of a built-in service, and a reset to the
  // defaults. Both read and take a time the way the settings do, such as 10m,
  // 1h or 1d, and save on Enter or blur like every other field here.
  import SettingRow from '../ui/SettingRow.svelte'
  import SettingControl from './SettingControl.svelte'
  import { settingDisplay } from '../settings/display.ts'
  import { seconds } from './model.ts'
  import { minutesText } from '../ui/format.ts'
  import type { RegistryService, ServicePatch } from '../api/service.ts'
  import type { SettingItem } from '../api/settings.ts'

  interface Props {
    schedule: NonNullable<RegistryService['schedule']>
    busy: boolean
    // onsave resolves true when the engine took the change.
    onsave: (patch: ServicePatch) => Promise<boolean>
    onreset: () => Promise<unknown>
  }
  let { schedule, busy, onsave, onreset }: Props = $props()

  let errors = $state<Record<string, string>>({})
  const field = (key: string, display: string): SettingItem => ({ key, value: display, display, kind: 'duration', source: 'file', liveVsRestart: '', help: '' })

  // A time the engine cannot take rejects, so the field keeps the text for
  // the owner to correct. The interval runs in whole minutes; the time limit
  // may take seconds.
  async function save(key: string, text: string, patch: ServicePatch | null, hint: string): Promise<void> {
    errors = { ...errors, [key]: patch ? '' : hint }
    if (!patch || !(await onsave(patch))) throw new Error(hint)
  }
  function every(text: string): ServicePatch | null {
    const n = seconds(text)
    return n && n % 60 === 0 ? { everyMinutes: n / 60 } : null
  }
  function limit(text: string): ServicePatch | null {
    const n = seconds(text)
    return n ? { timeout: `${n}s` } : null
  }
</script>

<SettingRow title="Runs every" description="Time between runs">
  {#snippet control()}<SettingControl item={field('every', minutesText(schedule.everyMinutes))} label="Time between runs" onsave={(v: string) => save('every', v, every(v), 'Use whole minutes, such as 10m, 1h or 1d')} />{/snippet}
  {#snippet status()}{#if errors.every}<span class="err" role="alert">{errors.every}</span>{/if}{/snippet}
</SettingRow>
<SettingRow title="Time limit" description="Longest one run may take">
  {#snippet control()}<SettingControl item={field('limit', settingDisplay({ kind: 'duration', value: schedule.timeout }))} label="Time limit of one run" onsave={(v: string) => save('limit', v, limit(v), 'Use a time such as 30s, 10m or 1h')} />{/snippet}
  {#snippet status()}{#if errors.limit}<span class="err" role="alert">{errors.limit}</span>{/if}{/snippet}
</SettingRow>
<SettingRow title="Defaults" description="Restores the switch, the interval and the time limit">
  {#snippet control()}<button class="btn" disabled={busy} onclick={onreset}>Reset</button>{/snippet}
</SettingRow>

<style>
  .err { color: var(--tone-danger-fg); }
</style>
