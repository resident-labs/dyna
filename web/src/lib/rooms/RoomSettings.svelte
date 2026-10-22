<script>
  // Room settings: the shared settings modal with one pane per section,
  // opened from the room menu or the palette. Each control calls the same
  // API as the CLI. The room policy (review and CI time) saves when a field
  // commits, like the Settings pane. The review switch and the policy's
  // review field are one setting: the host keeps both in step, so the policy
  // reloads when the switch flips.
  import { tick, untrack } from 'svelte'
  import { api } from '../../api.js'
  import { roomLabel } from '../ui/format.js'
  import Switch from '../ui/Switch.svelte'
  import SettingsShell from '../ui/SettingsShell.svelte'
  import ServiceHeader from '../services/ServiceHeader.svelte'
  import HooksPane from '../settings/HooksPane.svelte'
  import CIBudgetFields from '../prs/CIBudgetFields.svelte'
  import PRNoiseRules from '../prs/PRNoiseRules.svelte'
  import { normalizePolicy, parseChecks, checksText, saveRoomPolicy, budgetForm, parseTestAllow } from '../prs/pr.ts'
  import Section from './RoomSettingsSection.svelte'
  import Group from './RoomSettingsGroup.svelte'
  import Row from './RoomSettingsRow.svelte'

  // start: the first section. reviewer: the ReviewerStatus answer;
  // onreviewer reloads it. controls: the room session controls (pause
  // state); onsessions runs 'pause' or 'resume'. onaction runs a room action
  // by id: rename, room-pin, room-archive or delete. rules draws the Rules
  // section; busy is true while a rule edit is open, so Esc cancels the edit.
  let { room, start = 'general', reviewer = null, onreviewer = () => {}, controls = null, onsessions = () => {}, onaction = () => {}, rules, busy: rulesBusy = false, onclose } = $props()
  const nav = [
    { id: 'general', label: 'General' },
    { id: 'review', label: 'Review & landing' },
    { id: 'ci', label: 'CI time' },
    { id: 'services', label: 'Services' },
    { id: 'rules', label: 'Rules' },
    { id: 'hooks', label: 'Hooks' },
    { id: 'noise', label: 'Bot noise rules' },
  ]
  let section = $state(untrack(() => start))
  let hooksBusy = $state(false) // a hooks dialog owns Esc while open
  let roomId = $derived(room.id)
  let reviewEnabled = $derived(reviewer?.enabled ?? null)

  let policy = $state(null)
  let form = $state(null)
  let busy = $state(false)
  let error = $state('')
  let saved = $state(false)
  let reviewerBusy = $state('')

  let policyRequest = 0
  let policyRoom = ''
  let policyLoading = $state(false)
  let saveAfterPolicy = false
  const formOf = (p) => ({ ...budgetForm(p), requiredChecks: checksText(p.requiredChecks) })
  const fill = (p) => {
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
      const edits = {}
      if (policy && form) {
        const base = formOf(policy)
        for (const key of Object.keys(form)) {
          if (JSON.stringify(form[key]) !== JSON.stringify(base[key])) edits[key] = form[key]
        }
      }
      fill(next)
      Object.assign(form, edits)
      error = ''
    } catch (e) {
      if (request === policyRequest && targetRoom === roomId) error = String(e?.message || e)
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
    if (!form || !policy) return false
    const base = budgetForm(policy)
    const fields = ['checks', 'review', 'land', 'ciTimeBudget', 'ciWarnAfter', 'ciFailAfter', 'ciTestMax', 'ciNoJUnitWarning']
    return fields.some((k) => form[k] !== base[k]) ||
      parseChecks(form.requiredChecks).join('\n') !== policy.requiredChecks.join('\n') ||
      parseTestAllow(form.ciBudgetExempt).join('\n') !== (policy.ciBudgetExempt ?? []).join('\n') ||
      parseTestAllow(form.ciTestAllow).join('\n') !== (policy.ciTestAllow ?? []).join('\n') ||
      parseTestAllow(form.ciTestAllow).some((id) => (form.ciTestAllowNotes[id] ?? '') !== (base.ciTestAllowNotes[id] ?? ''))
  })
  async function save() {
    if (busy || !dirty) return
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
      if (targetRoom === roomId) error = String(e?.message || e)
    } finally {
      busy = false
    }
  }

  async function reviewerAct(name, fn) {
    reviewerBusy = name
    error = ''
    try {
      await fn()
      await onreviewer()
    } catch (e) {
      error = String(e?.message || e)
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
  const flip = () => reviewerAct('enabled', () => api.setReviewEnabled(roomId, !reviewer.enabled))
  const restart = () => reviewerAct('restart', () => api.restartReviewer(roomId))

  // Where reviews appear: the room's PR settings. The old reviewer fields
  // pass through untouched; the backend owns them.
  let prSettings = $state(null)
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
      publishError = String(e?.message || e)
    }
  }
  async function savePublishing() {
    const next = { reviewerProfile: prSettings?.reviewerProfile || '', reviewerSession: prSettings?.reviewerSession || '', reviewPublishing, githubAccess, mergeMaxParallel }
    try {
      await api.setPRSettings(roomId, next)
      prSettings = next
      publishError = ''
    } catch (e) {
      publishError = String(e?.message || e)
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

<SettingsShell label="{roomLabel(room)} settings" title="{roomLabel(room)} settings" {nav} pane={section} onselect={(id) => (section = id)} busy={rulesBusy || hooksBusy} {onclose}>
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
        <Row title="Review new PRs" help="A reviewer agent reviews each tracked PR. Choose its model in Services.">
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
            {#snippet control()}<input class="field" aria-label="Required checks" type="text" bind:value={form.requiredChecks} disabled={busy} onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur() }} />{/snippet}
          </Row>
          <Row title="GitHub access" help="Read-only lets Dyna read PRs, CI and issues. Read and write also lets Dyna comment, change PRs and merge. New agent turns use this access." error={publishError}>
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
{:else if section === 'services'}
  <Section title="Services" lede="The agents that work for this room. Pick Auto or one profile.">
    <Group>
      <div class="svc"><ServiceHeader {roomId} role="reviewer" onchange={onreviewer} /></div>
      <div class="svc"><ServiceHeader {roomId} role="keeper" /></div>
    </Group>
  </Section>
{:else if section === 'rules'}
  {@render rules()}
{:else if section === 'hooks'}
  <Section title="Hooks" lede="Shell commands that Dyna runs on repository events, such as after a pull request merges.">
    {#if room.repo}<HooksPane repo={room.repo} onmodal={(open) => (hooksBusy = open)} />{:else}<p class="quiet">This room has no repository.</p>{/if}
  </Section>
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
  .svc :global(.bar) { padding: var(--s3) 18px; border-bottom: 0; }
  .err { color: var(--coral); font-size: var(--t-ui); margin: var(--s3) 0 0; }
  .quiet { color: var(--faint); font-size: var(--t-ui); }
  @media (max-width: 760px) {
    .grid { grid-template-columns: 1fr; }
    .field { font-size: var(--t-title); min-height: 44px; }
    .ghost { min-height: 44px; }
  }
</style>
