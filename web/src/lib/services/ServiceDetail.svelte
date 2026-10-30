<script lang="ts">
  // One service's detail, opened in place under its row: its timing, the
  // settings keys the registry lists for it, Run now where the service runs on
  // request, and what it did lately. Local CI shows the room's CI health.
  import { api } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import Card from '../ui/Card.svelte'
  import SettingRow from '../ui/SettingRow.svelte'
  import CIHealth from '../cihealth/CIHealth.svelte'
  import EntryList from './EntryList.svelte'
  import ScheduleControls from './ScheduleControls.svelte'
  import SettingControl from './SettingControl.svelte'
  import { settingLabel, setupHint, switchHint } from './model.ts'
  import type { ControlHost } from './control.ts'
  import { hookEntries, mergeEntries, reviewEntries, type ChangeRow, type Entry, type RunReason } from './activity.ts'
  import { readHistory, readHookLog, readHookRuns } from './read.ts'
  import type { RegistryService, ServicePatch } from '../api/service.ts'
  import type { SettingItem } from '../api/settings.ts'

  interface Props {
    service: RegistryService
    host: ControlHost
    settings: SettingItem[] | null
    changes: ChangeRow[]
    // canOpen says whether a session address opens in this room.
    canOpen: (session: string) => boolean
    onopen: (session: string) => void
    onpr?: (pr: number) => void
    // onchanged reads the registry again after a write.
    onchanged: () => Promise<void>
    // onsetting saves one settings key and reads the settings again.
    onsetting: (key: string, value: string) => Promise<void>
  }
  let { service, host, settings, changes, canOpen, onopen, onpr, onchanged, onsetting }: Props = $props()

  // A built-in pass keeps its runs on the machine under its own name.
  const hook = $derived(service.schedule ? service.id : '')
  const items = $derived((settings ?? []).filter((it) => service.settingsKeys.includes(it.key)))
  let busy = $state(false)
  let error = $state('')
  let note = $state('')
  let errors = $state<Record<string, string>>({})
  let entries = $state<Entry[] | null>(null)
  let logs = $state<Record<string, string>>({})
  let shown = $state('')

  // write runs one change, shows a refusal inline, and reads the registry again.
  async function write(work: () => Promise<unknown>, done = ''): Promise<void> {
    busy = true
    error = ''
    note = ''
    await work().then(() => { note = done }, (e: unknown) => { error = errorText(e) })
    // The new registry row reruns loadActivity, so the new run shows.
    await onchanged()
    busy = false
  }

  async function saveSetting(key: string, value: string): Promise<void> {
    errors = { ...errors, [key]: '' }
    await onsetting(key, value).catch((e: unknown) => { errors = { ...errors, [key]: errorText(e) } })
  }

  // The runs of a hook, with the log reason of each failure among them.
  async function hookActivity(name: string): Promise<Entry[]> {
    const runs = readHookRuns(await api.globalHookRuns(name)).slice(0, 20)
    const reasons: Record<string, RunReason> = {}
    await Promise.all(runs.filter((r) => r.result === 'error' || r.result === 'fail').map(async (r) => {
      const reason = readHookLog(await logOf(r.id))
      if (reason) reasons[r.id] = reason
    }))
    return hookEntries(name, service.label, runs, Date.now(), reasons)
  }

  const logOf = (id: string) => api.globalHookRunLog(hook, id)

  function loadActivity(): void {
    const read = service.id === 'merge-queue' ? api.roomMergeHistory(host.room).then((v) => mergeEntries(readHistory(v)))
      : service.id === 'reviewer' ? Promise.resolve(reviewEntries(changes))
      : hook ? hookActivity(hook)
      : Promise.resolve([])
    read.then((list) => { entries = list }, (e: unknown) => { entries = []; error = errorText(e) })
  }
  $effect(loadActivity)

  // A failed run opens its log tail in place.
  async function toggleLog(run: { id: string }): Promise<void> {
    shown = shown === run.id ? '' : run.id
    if (shown && logs[run.id] === undefined) logs = { ...logs, [run.id]: (await logOf(run.id).catch(() => null))?.tail ?? 'The log could not be read.' }
  }
</script>

<div class="detail">
  {#if service.schedule || items.length}
    <Card flush divided>
      {#if service.schedule}
        <ScheduleControls schedule={service.schedule} {busy}
          onsave={(patch: ServicePatch) => write(() => api.editService(service.id, patch), 'Saved.')}
          onreset={() => write(() => api.resetService(service.id), 'Back to the defaults.')} />
      {/if}
      {#each items as it (it.key)}
        {@const text = settingLabel(it.key, it.help)}
        <SettingRow title={text.title} description={text.description}>
          {#snippet control()}<SettingControl item={it} label={text.title} onsave={(value: string) => saveSetting(it.key, value)} />{/snippet}
          {#snippet status()}{#if errors[it.key]}<span class="err" role="alert">{errors[it.key]}</span>{/if}{/snippet}
        </SettingRow>
      {/each}
    </Card>
  {/if}
  {#if service.state === 'not-set-up' && setupHint[service.id]}<p class="hint">{setupHint[service.id]}</p>{/if}
  {#if switchHint[service.id]}<p class="hint">{switchHint[service.id]}</p>{/if}
  {#if service.schedule || canOpen(service.session)}
    <div class="acts">
      {#if service.schedule}<button class="btn" disabled={busy} onclick={() => write(() => api.runService(service.id), `${service.label} ran.`)}>Run now</button>{/if}
      {#if canOpen(service.session)}<button class="btn quiet" onclick={() => onopen(service.session)}>Open session</button>{/if}
      {#if note}<span class="note" role="status">{note}</span>{/if}
    </div>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  {#if service.id === 'local-ci'}
    <CIHealth roomId={host.room} {changes} {onpr} />
  {:else if entries === null || entries.length || hook}
    <h4 class="micro-label">Recent runs</h4>
    {#if entries === null}
      <p class="hint">Reading…</p>
    {:else}
      <EntryList {entries} empty="No runs yet." onOpen={onopen} onRun={toggleLog} />
      {#if shown}<pre class="log mono">{logs[shown] ?? 'Reading the log…'}</pre>{/if}
    {/if}
  {/if}
</div>

<style>
  .detail { display: flex; flex-direction: column; gap: var(--s3); padding-top: var(--s3); border-top: 1px solid var(--border-soft); }
  .micro-label { margin: var(--s1) 0 0; }
  .acts { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .hint, .note { margin: 0; font-size: var(--t-sm); color: var(--muted); }
  .err { margin: 0; font-size: var(--t-sm); color: var(--tone-danger-fg); }
  .log { margin: 0; padding: var(--s2) var(--s3); max-height: 16rem; overflow: auto; white-space: pre-wrap; word-break: break-all; background: var(--rail); border-radius: var(--r-xs); font-size: var(--t-sm); color: var(--muted); }
</style>
