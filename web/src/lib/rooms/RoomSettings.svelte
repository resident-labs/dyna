<script lang="ts">
  // Room settings: the shared settings modal with one pane per section,
  // opened from the room menu or the palette. Each control calls the same
  // API as the CLI. The room policy (review and CI time) saves when a field
  // commits, like the Settings pane. The review switch and the policy's
  // review field are one setting: the host keeps both in step, so the policy
  // reloads when the switch flips.
  import { tick, untrack } from 'svelte'
  import type { Snippet } from 'svelte'
  import { api } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import { roomLabel } from '../ui/format.ts'
  import Switch from '../ui/Switch.svelte'
  import SettingsShell from '../ui/SettingsShell.svelte'
  import CIBudgetFields from '../prs/CIBudgetFields.svelte'
  import PRNoiseRules from '../prs/PRNoiseRules.svelte'
  import { normalizePolicy, parseChecks, checksText, saveRoomPolicy, budgetForm, parseTestAllow } from '../prs/pr.ts'
  import type { BudgetForm, PolicyForm, NormalizedPolicy } from '../prs/pr.ts'
  import type { Room, RoomLimits, ProfileChoice, RoomLimitField } from '../api/room.ts'
  import type { ChangePolicy, PRConfig, ReviewerStatus } from '../api/change.ts'
  import type { RoomControlValue } from './roomSessionControls.ts'
  import Section from './RoomSettingsSection.svelte'
  import Group from './RoomSettingsGroup.svelte'
  import Row from './RoomSettingsRow.svelte'

  // controls is the room session controls (RoomControlValue). The app keeps a
  // narrower copy, so every field is optional here.
  type Controls = Partial<Pick<RoomControlValue, 'busy' | 'loading' | 'error' | 'statusError'>> & {
    state?: { paused?: boolean; dormant?: number } | null
  }

  // The policy form this pane edits: every budget field, plus the checks text.
  type PolicyDraft = BudgetForm & Pick<PolicyForm, 'requiredChecks' | 'review'>

  interface Props {
    // start: the first section. reviewer: the ReviewerStatus answer;
    // onreviewer reloads it. controls: the room session controls (pause
    // state); onsessions runs 'pause' or 'resume'. onaction runs a room action
    // by id: rename, room-pin, room-archive or delete. rules draws the Rules
    // section; busy is true while a rule edit is open, so Esc cancels the edit.
    // onhooks shows Services > Hooks, where the repository's hooks live.
    room: Room
    profiles?: ProfileChoice[]
    onrootprofile?: (roomId: string, profile: string) => void
    oncheckin?: (roomId: string, value: string) => void
    onlimit?: (roomId: string, field: RoomLimitField, value: string) => void
    start?: string
    reviewer?: ReviewerStatus | null
    onreviewer?: () => unknown
    controls?: Controls | null
    onsessions?: (action: string) => void
    onaction?: (id: string) => void
    rules: Snippet
    busy?: boolean
    onhooks?: () => unknown
    onclose: () => void
  }
  let { room, profiles = [], onrootprofile = () => {}, oncheckin = () => {}, onlimit = () => {}, start = 'general', reviewer = null, onreviewer = () => {}, controls = null, onsessions = () => {}, onaction = () => {}, rules, busy: rulesBusy = false, onhooks = () => {}, onclose }: Props = $props()
  let rootProfile = $state(untrack(() => room.rootProfile || ''))
  let rootProfileBusy = $state(false)
  let rootProfileError = $state('')
  $effect(() => { rootProfile = room.rootProfile || '' })

  async function saveRootProfile() {
    const target = room.id
    const value = rootProfile
    rootProfileBusy = true
    rootProfileError = ''
    try {
      await api.setRoomRootProfile(target, value)
      onrootprofile(target, value)
    } catch (e) {
      if (room.id === target) {
        rootProfile = room.rootProfile || ''
        rootProfileError = errorText(e)
      }
    } finally {
      rootProfileBusy = false
    }
  }

  // The parent check-in field takes the same value as the CLI: a duration,
  // off, or empty for the hook's global interval. It saves when it commits.
  let checkIn = $state(untrack(() => room.checkIn || ''))
  let checkInBusy = $state(false)
  let checkInError = $state('')
  $effect(() => { checkIn = room.checkIn || '' })

  async function saveCheckIn() {
    const target = room.id
    const value = checkIn.trim()
    checkInBusy = true
    checkInError = ''
    try {
      await api.setRoomCheckIn(target, value)
      checkIn = value
      oncheckin(target, value)
    } catch (e) {
      if (room.id === target) {
        checkIn = room.checkIn || ''
        checkInError = errorText(e)
      }
    } finally {
      checkInBusy = false
    }
  }
  // The limit fields take the same values as the CLI: a size such as 2G for
  // memory, a count such as 512 for tasks, off, or empty for the machine
  // default. Each saves when it commits.
  let limits = $state<RoomLimits>(untrack(() => ({ ...room.limits })))
  let limitBusy = $state<Record<string, boolean>>({})
  let limitError = $state<Record<string, string>>({})
  $effect(() => { limits = { ...room.limits } })

  async function saveLimit(field: RoomLimitField) {
    const target = room.id
    const value = (limits[field] ?? '').trim()
    limitBusy = { ...limitBusy, [field]: true }
    limitError = { ...limitError, [field]: '' }
    try {
      const res = await api.setRoomLimit(target, field, value)
      const saved = { ...limits }
      saved[field] = value
      limits = saved
      onlimit(target, field, value)
      // The saved value is what the room keeps; a live-apply failure is a
      // warning on the kept value, not a failed save.
      if (res.applyWarning) limitError = { ...limitError, [field]: res.applyWarning }
    } catch (e) {
      if (room.id === target) {
        const stored = { ...limits }
        stored[field] = room.limits?.[field] ?? ''
        limits = stored
        limitError = { ...limitError, [field]: errorText(e) }
      }
    } finally {
      limitBusy = { ...limitBusy, [field]: false }
    }
  }
  const nav: { id: string; label: string }[] = [
    { id: 'general', label: 'General' },
    { id: 'review', label: 'Review & landing' },
    { id: 'ci', label: 'CI time' },
    { id: 'rules', label: 'Rules' },
    { id: 'hooks', label: 'Hooks' },
    { id: 'noise', label: 'Bot noise rules' },
  ]
  let section = $state(untrack(() => start))
  let roomId = $derived(room.id)
  let reviewEnabled = $derived(reviewer?.enabled ?? null)

  let policy = $state<NormalizedPolicy | null>(null)
  let form = $state<PolicyDraft | null>(null)
  let busy = $state(false)
  let error = $state('')
  let saved = $state(false)
  let reviewerBusy = $state('')

  let policyRequest = 0
  let policyRoom = ''
  let policyLoading = $state(false)
  let saveAfterPolicy = false
  const formOf = (p: Partial<ChangePolicy>): PolicyDraft => ({ ...budgetForm(p), requiredChecks: checksText(p.requiredChecks) })
  // differing lists the form fields a fresh policy would not set on its own.
  function differing(base: PolicyDraft, edited: PolicyDraft) {
    const baseValues = new Map<string, unknown>(Object.entries(base))
    return Object.fromEntries(Object.entries(edited).filter(([key, value]) => JSON.stringify(value) !== JSON.stringify(baseValues.get(key))))
  }
  const fill = (p: Partial<ChangePolicy>) => {
    policy = normalizePolicy(p)
    form = formOf(policy)
    policyRoom = roomId
  }
  async function load() {
    const request = ++policyRequest
    const targetRoom = roomId
    policyLoading = true
    if (policyRoom !== targetRoom) {
      policy = null
      form = null
      saved = false
      error = ''
      saveAfterPolicy = false
      policyRoom = targetRoom
    }
    try {
      const next = await api.roomChangePolicy(targetRoom)
      if (request !== policyRequest || targetRoom !== roomId) return
      const edits = form && policy ? differing(formOf(policy), form) : {}
      fill(next)
      if (form) Object.assign(form, edits)
      error = ''
    } catch (e) {
      if (request === policyRequest && targetRoom === roomId) error = errorText(e)
    } finally {
      if (request === policyRequest && targetRoom === roomId) {
        policyLoading = false
        if (saveAfterPolicy && !error) {
          saveAfterPolicy = false
          await save()
        }
      }
    }
  }
  $effect(() => {
    reviewEnabled
    if (section === 'review' || section === 'ci') load()
  })

  let dirty = $derived.by(() => {
    const editing = form
    const current = policy
    if (!editing || !current) return false
    const base = budgetForm(current)
    const fields: (keyof PolicyDraft)[] = ['review', 'ciTimeBudget', 'ciWarnAfter', 'ciFailAfter', 'ciTestMax', 'ciNoJUnitWarning']
    return fields.some((k) => editing[k] !== base[k]) ||
      parseChecks(editing.requiredChecks).join('\n') !== current.requiredChecks.join('\n') ||
      parseTestAllow(editing.ciBudgetExempt).join('\n') !== (current.ciBudgetExempt ?? []).join('\n') ||
      parseTestAllow(editing.ciTestAllow).join('\n') !== (current.ciTestAllow ?? []).join('\n') ||
      parseTestAllow(editing.ciTestAllow).some((id) => (editing.ciTestAllowNotes[id] ?? '') !== (base.ciTestAllowNotes[id] ?? ''))
  })
  async function save() {
    if (!form || !policy || busy || !dirty) return
    if (policyLoading || reviewerBusy) {
      saveAfterPolicy = true
      return
    }
    if (saveAfterPolicy) {
      await load()
      return
    }
    const targetRoom = roomId
    policyRequest += 1
    busy = true
    error = ''
    saved = false
    try {
      const next = await saveRoomPolicy(api, targetRoom, form, policy)
      if (targetRoom !== roomId) return
      policyRequest += 1
      policyLoading = false
      fill(next)
      saved = true
      await onreviewer()
    } catch (e) {
      if (targetRoom === roomId) error = errorText(e)
    } finally {
      busy = false
    }
  }

  async function reviewerAct(name: string, fn: () => Promise<unknown>) {
    reviewerBusy = name
    error = ''
    try {
      await fn()
      await onreviewer()
    } catch (e) {
      error = errorText(e)
    } finally {
      reviewerBusy = ''
      if (saveAfterPolicy) {
        await tick()
        if (!policyLoading && !error) {
          saveAfterPolicy = false
          await save()
        }
      }
    }
  }
  const flip = () => reviewerAct('enabled', () => api.setReviewEnabled(roomId, !reviewer?.enabled))
  const restart = () => reviewerAct('restart', () => api.restartReviewer(roomId))

  // Where reviews appear: the room's PR settings. The old reviewer fields
  // pass through untouched; the backend owns them.
  let prSettings = $state<Partial<PRConfig> | null>(null)
  let reviewPublishing = $state('local')
  let githubAccess = $state('read-only')
  let mergeMaxParallel = $state(3)
  let publishError = $state('')
  async function loadPublishing() {
    try {
      prSettings = (await api.prSettings(roomId)) ?? {}
      reviewPublishing = prSettings.reviewPublishing === 'github' ? 'github' : 'local'
      githubAccess = prSettings.githubAccess === 'read-write' ? 'read-write' : 'read-only'
      mergeMaxParallel = prSettings.mergeMaxParallel || 3
      publishError = ''
    } catch (e) {
      publishError = errorText(e)
    }
  }
  async function savePublishing() {
    const next: PRConfig = { reviewerProfile: prSettings?.reviewerProfile || '', reviewerSession: prSettings?.reviewerSession || '', reviewPublishing, githubAccess, mergeMaxParallel }
    try {
      await api.setPRSettings(roomId, next)
      prSettings = next
      publishError = ''
    } catch (e) {
      publishError = errorText(e)
      reviewPublishing = prSettings?.reviewPublishing === 'github' ? 'github' : 'local'
      githubAccess = prSettings?.githubAccess === 'read-write' ? 'read-write' : 'read-only'
      mergeMaxParallel = prSettings?.mergeMaxParallel || 3
    }
  }
  $effect(() => { if (section === 'review') loadPublishing() })

  let paused = $derived(!!controls?.state?.paused)
</script>

{#snippet policyError()}
  {#if error}
    <div role="alert">
      <p class="err">{error}</p>
      <button class="ghost" disabled={busy || policyLoading || !!reviewerBusy} onclick={dirty ? save : load}>{dirty ? 'Retry save' : 'Retry'}</button>
    </div>
  {/if}
{/snippet}

<SettingsShell label="{roomLabel(room)} settings" title="{roomLabel(room)} settings" {nav} pane={section} onselect={(id) => (id === 'hooks' ? onhooks() : (section = id))} busy={rulesBusy} {onclose}>
{#if section === 'general'}
  <Section title="General" lede="The room itself.">
    <Group>
      <Row title="Name" help="Agents still address the room as #{room.name}.">
        {#snippet control()}<span class="value">{roomLabel(room)}</span><button class="ghost" onclick={() => onaction('rename')}>Rename…</button>{/snippet}
      </Row>
      <Row title="Pin to rail" help="Pinned rooms stay at the top of the rail.">
        {#snippet control()}<Switch on={!!room.pinned} label="Pin to rail" onchange={() => onaction('room-pin')} />{/snippet}
      </Row>
      <Row title="Pause agents" help={paused ? 'Paused. New turns and messages wait until you resume.' : 'Holds new turns and messages until you resume.'} error={controls?.error || controls?.statusError || ''}>
        {#snippet control()}<Switch on={paused} label="Pause agents" onchange={() => onsessions(paused ? 'resume' : 'pause')} disabled={!controls?.state || controls.busy} />{/snippet}
      </Row>
      <Row title="Root profile" help="Used for the next new root session in this room. Existing sessions keep their profile." error={rootProfileError}>
        {#snippet control()}
          <select class="field" aria-label="Root profile" bind:value={rootProfile} onchange={saveRootProfile} disabled={rootProfileBusy}>
            <option value="">Global default</option>
            {#if rootProfile && !profiles.some((p) => p.name === rootProfile)}<option value={rootProfile} disabled>{rootProfile} (unavailable)</option>{/if}
            {#each profiles as p}<option value={p.name}>{p.name}</option>{/each}
          </select>
        {/snippet}
      </Row>
      <Row title="Parent check-ins" help="How often Dyna wakes a parent to check its children in this room: a duration such as 30m, off, or empty for the global interval." error={checkInError}>
        {#snippet control()}
          <input class="field" aria-label="Parent check-ins" type="text" placeholder="default" spellcheck="false" bind:value={checkIn} disabled={checkInBusy} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} onchange={saveCheckIn} />
        {/snippet}
      </Row>
      <Row title="Memory high" help="Where the kernel starts to throttle this room's processes: a size such as 2G, off, or empty for the machine default." error={limitError.memoryHigh}>
        {#snippet control()}
          <input class="field" aria-label="Memory high" type="text" placeholder="default" spellcheck="false" bind:value={limits.memoryHigh} disabled={limitBusy.memoryHigh} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} onchange={() => saveLimit('memoryHigh')} />
        {/snippet}
      </Row>
      <Row title="Memory max" help="The hard memory limit: the kernel kills a process of this room, never outside it. A size such as 2G, off, or empty for the machine default." error={limitError.memoryMax}>
        {#snippet control()}
          <input class="field" aria-label="Memory max" type="text" placeholder="default" spellcheck="false" bind:value={limits.memoryMax} disabled={limitBusy.memoryMax} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} onchange={() => saveLimit('memoryMax')} />
        {/snippet}
      </Row>
      <Row title="Tasks" help="The most processes and threads this room may run: a count such as 512, off, or empty for the machine default." error={limitError.tasksMax}>
        {#snippet control()}
          <input class="field" aria-label="Tasks" type="text" placeholder="default" spellcheck="false" bind:value={limits.tasksMax} disabled={limitBusy.tasksMax} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} onchange={() => saveLimit('tasksMax')} />
        {/snippet}
      </Row>
    </Group>
    <Group title="Danger zone">
      <Row title="Archive" help="Hides the room from the rail. Nothing is deleted.">
        {#snippet control()}<button class="ghost" onclick={() => onaction('room-archive')}>{room.archived ? 'Unarchive' : 'Archive room'}</button>{/snippet}
      </Row>
      <Row title="Delete" help="Removes the room, its feed and its pins.">
        {#snippet control()}<button class="ghost danger" onclick={() => onaction('delete')}>Delete room…</button>{/snippet}
      </Row>
    </Group>
  </Section>
{:else if section === 'review'}
  <Section title="Review & landing" lede="How changes in this room get reviewed and merged.">
    {#if reviewer}
      <Group title="Automatic review">
        <Row title="Review new PRs" help="A reviewer agent reviews each tracked PR. Choose its model in Settings > Settings > Services.">
          {#snippet control()}<Switch on={reviewer.enabled} label="Automatic review" onchange={flip} disabled={!!reviewerBusy || busy} />{/snippet}
        </Row>
        <Row title="Reviewer sessions" help="Close the reviewer sessions and start new ones.">
          {#snippet control()}<button class="ghost" onclick={restart} disabled={!!reviewerBusy || busy}>{reviewerBusy === 'restart' ? 'Restarting…' : 'Restart reviewer'}</button>{/snippet}
        </Row>
      </Group>
    {/if}
    {#if form}
      <!-- Every field saves when it commits; change events bubble here. -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div onchange={save}>
        <Group title="Review">
          <Row title="Required checks" help="GitHub check names, separated by commas.">
            {#snippet control()}{#if form}<input class="field" aria-label="Required checks" type="text" bind:value={form.requiredChecks} disabled={busy} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} />{/if}{/snippet}
          </Row>
          <Row title="GitHub access" help="Read-only lets Dyna read pull requests, checks and issues. Read and write also lets Dyna push, comment, change pull requests and merge. This policy applies to this room." error={publishError}>
            {#snippet control()}<select class="field" aria-label="GitHub access" bind:value={githubAccess} onchange={savePublishing} disabled={!prSettings}><option value="read-only">Read-only</option><option value="read-write">Read and write</option></select>{/snippet}
          </Row>
          <Row title="Parallel prefix trials" help="Maximum active prefix CI runs after a batch fails. Default: 3." error={publishError}>
            {#snippet control()}<input class="field" aria-label="Parallel prefix trials" type="number" min="1" step="1" value={mergeMaxParallel} onchange={(e) => { e.stopPropagation(); mergeMaxParallel = Number(e.currentTarget.value); if (Number.isInteger(mergeMaxParallel) && mergeMaxParallel > 0) savePublishing(); else publishError = 'Enter a positive integer' }} disabled={!prSettings} />{/snippet}
          </Row>
          <Row title="Where reviews appear" help="Applies to future reviews. Existing GitHub feedback stays public." error={publishError}>
            {#snippet control()}<select class="field" aria-label="Where reviews appear" value={reviewPublishing} onchange={(e) => { e.stopPropagation(); reviewPublishing = e.currentTarget.value; savePublishing() }} disabled={!prSettings}><option value="local">In Dyna only</option><option value="github">On GitHub as review comments</option></select>{/snippet}
          </Row>
        </Group>
        <p class="quiet">Dyna tests approved pull requests together before it merges the batch. Failed batches use prefix trials to find a passing group.</p>
      </div>
    {/if}
    {@render policyError()}
  </Section>
{:else if section === 'ci'}
  <Section title="CI time" lede="Time limits for pull request runs and tests. Dyna cancels a run that passes the failure limit.">
    {#if form}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div onchange={save}>
        <Group>
          <Row title="Run and test budget" wide saved={saved && !dirty}>
            <div class="grid"><CIBudgetFields bind:form {busy} /></div>
          </Row>
        </Group>
      </div>
    {/if}
    {@render policyError()}
  </Section>
{:else if section === 'rules'}
  {@render rules()}
{:else if section === 'noise'}
  <Section title="Bot noise rules">
    {#if room.repo}<PRNoiseRules {roomId} />{:else}<p class="quiet">This room has no repository.</p>{/if}
  </Section>
{/if}
</SettingsShell>

<style>
  .field { width: 280px; max-width: 100%; background: var(--rail); border: 1px solid transparent; border-radius: 6px; color: var(--ink); padding: 7px 10px; font: inherit; font-size: var(--t-ui); outline: none; }
  .field:focus { border-color: var(--iris-edge); }
  .field:disabled { opacity: 0.5; }
  .value { margin-right: var(--s3); font-size: var(--t-body); color: var(--ink-2); }
  .ghost { font-size: var(--t-ui); color: var(--ink); border: 1px solid var(--line); border-radius: 6px; padding: 5px 10px; }
  .ghost:not(:disabled) { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ghost:hover:not(:disabled) { background: var(--hover); }
  }
  .ghost:active:not(:disabled) { background: var(--select); }
  .ghost:disabled { opacity: 0.5; }
  .ghost.danger { color: var(--coral); }
  .grid { display: grid; grid-template-columns: 160px 1fr; gap: var(--s2) var(--s3); align-items: center; }
  .err { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .quiet { color: var(--faint); font-size: var(--t-ui); }
  @media (max-width: 760px) {
    .grid { grid-template-columns: 1fr; }
    .field { font-size: var(--t-title); min-height: 44px; }
    .ghost { min-height: 44px; }
  }
</style>
