<script lang="ts">
  // Worker profiles: list, edit, add, make default, enable or disable, and
  // remove. Changes that can stop work elsewhere ask first.
  import { errorText } from '../errorText.ts'
  import { onMount } from 'svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../ui/motion.ts'
  import TabHeader from '../rooms/TabHeader.svelte'
  import { api } from '../../api.ts'
  import ProfileEditor from './ProfileEditor.svelte'
  import ProfileUses from './ProfileUses.svelte'
  import ProfileWizard from './ProfileWizard.svelte'
  import Confirm from '../ui/Confirm.svelte'
  import Menu from '../ui/Menu.svelte'
  import SupportBrief from './SupportBrief.svelte'
  import { healthChip } from './support.ts'
  import { costText, rangeBounds, type Period } from './accounts/activity.ts'
  import type { ProfileCheck, ProfileList, ProfileUsageSnapshot, ProfileUsageStats, ProfileView } from '../api/profile.ts'
  import type { EditableProfile, ModelSelection } from './profiles/state.ts'

  type ConfirmKind = 'default' | 'disable' | 'enable' | 'remove'
  // The list with every part present, so a view reads it without a null check.
  type Loaded = { [K in keyof ProfileList]-?: NonNullable<ProfileList[K]> }
  interface Props {
    onmodal?: (open: boolean) => void
    initialSelection?: ModelSelection | null
    initialProfile?: string
    roomId?: string
    onusage?: (accountId: string) => void
  }

  let { onmodal, initialSelection = null, initialProfile = '', roomId = '', onusage }: Props = $props()

  let creating = $state(false)
  let wizardSelection = $state<ModelSelection | null>(null)
  let lastSelection: ModelSelection | null = null
  let lastProfile = ''
  $effect(() => {
    if (initialSelection && initialSelection !== lastSelection) {
      lastSelection = initialSelection
      wizardSelection = initialSelection
      editing = null
      creating = true
    }
  })

  let list = $state<Loaded | null>(null)
  let error = $state('')
  let usagePeriod = $state<Period>('7d')
  let usage = $state<ProfileUsageSnapshot | null>(null)
  let usageError = $state('')
  let usageByName = $derived(new Map((usage?.profiles ?? []).map((row) => [row.profile, row] as const)))
  const usagePeriods: { value: Period; label: string }[] = [
    { value: 'today', label: 'Today' },
    { value: '7d', label: 'Last 7 days' },
    { value: '30d', label: 'Last 30 days' },
    { value: '90d', label: 'Last 90 days' },
  ]
  let filterQuery = $state('')
  let filterInput: HTMLInputElement | undefined = $state()
  let editing = $state<{ original: string; profile: ProfileView } | null>(null)
  let confirm = $state<{ kind: ConfirmKind; p: ProfileView; busy?: boolean; error?: string } | null>(null)
  let menu = $state<{ p: ProfileView; x: number; y: number } | null>(null)
  let note = $state('')
  let asking = $state<EditableProfile | null>(null) // the profile whose harness needs support

  // One color per harness, from the name palette, so a list reads at a glance.
  const harnessHue: Record<string, number> = { 'claude-code': 7, codex: 3, opencode: 6, muse: 2, mock: 0 }
  const short: Record<string, string> = { 'claude-code': 'CC', codex: 'CX', opencode: 'OC', muse: 'MU', mock: 'MK' }

  $effect(() => {
    if (initialProfile && initialProfile !== lastProfile && list) {
      lastProfile = initialProfile
      const profile = list.profiles.find(p => p.name === initialProfile)
      if (profile) {
        creating = false
        editing = { original: profile.name, profile: structuredClone($state.snapshot(profile)) }
      } else error = `Profile “${initialProfile}” is no longer available.`
    }
  })

  let enabled = $derived((list?.profiles ?? []).filter((p) => !p.disabled))
  let filterText = $derived(filterQuery.trim().toLowerCase())
  let filteredProfiles = $derived((list?.profiles ?? []).filter((p) => !filterText ||
    [p.name, p.harness, p.model, ...(p.models ?? []), p.description]
      .some((value) => value?.toLowerCase().includes(filterText))))

  $effect(() => { onmodal?.(!!(creating || editing || confirm || menu || asking)); return () => onmodal?.(false) })

  async function load() {
    try {
      const loaded: ProfileList = await api.configProfiles()
      list = { ...loaded, profiles: loaded.profiles ?? [], harnesses: loaded.harnesses ?? [], unready: loaded.unready ?? {} }
      error = ''
    } catch (e) {
      error = String(e)
    }
  }

  async function loadUsage() {
    const selected = usagePeriod
    const { from, to } = rangeBounds(selected)
    usage = null
    usageError = ''
    try {
      const loaded = await api.profileUsage(from, to)
      if (selected === usagePeriod) usage = loaded
    } catch (e) {
      if (selected === usagePeriod) usageError = errorText(e)
    }
  }

  function measuredNumber(value: number, unit: string) {
    return value.toFixed(1).replace(/\.0$/, '') + unit
  }

  function medianText(wallMs: number | null) {
    if (wallMs === null) return 'unknown'
    if (wallMs < 60_000) return measuredNumber(wallMs / 1_000, 's')
    if (wallMs < 3_600_000) return measuredNumber(wallMs / 60_000, 'm')
    return measuredNumber(wallMs / 3_600_000, 'h')
  }

  function costPerTurnText(measured: ProfileUsageStats) {
    const value = measured.costPerTurnUsd
    if (value === null) return 'unknown'
    const prefix = measured.cost.partialTurns > 0 ? '≥ ' : ''
    const digits = value > 0 && value < 0.01 ? 4 : 2
    return prefix + '$' + value.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })
  }

  function cacheText(measured: ProfileUsageStats) {
    return measured.cacheReadShare === null ? 'unknown' : measuredNumber(measured.cacheReadShare * 100, '%')
  }

  function costPerTurnTitle(measured: ProfileUsageStats) {
    const known = measured.cost.completeTurns + measured.cost.partialTurns
    const partial = measured.cost.partialTurns > 0 ? ' Partial reports make this average a lower bound.' : ''
    return `Average over ${known} of ${measured.turns} turns that report cost.${partial}`
  }

  function spendTitle(measured: ProfileUsageStats) {
    const known = measured.cost.completeTurns + measured.cost.partialTurns
    const partial = measured.cost.partialTurns > 0 || measured.cost.unknownTurns > 0 ? ' Partial or missing reports make total spend a lower bound.' : ''
    return `${known} of ${measured.turns} turns report cost.${partial}`
  }

  function say(text: string) {
    note = text
    setTimeout(() => { if (note === text) note = '' }, 2600)
  }

  function items(p: ProfileView) {
    const out: { label: string; danger?: boolean; gap?: boolean; run: () => void }[] = [{ label: 'Edit', run: () => (editing = { original: p.name, profile: structuredClone($state.snapshot(p)) }) }]
    if (!p.default && !p.disabled) out.push({ label: 'Make default', run: () => (confirm = { kind: 'default', p }) })
    out.push({ label: p.disabled ? 'Enable' : 'Disable', danger: !p.disabled, gap: true, run: () => (p.disabled ? act('enable', p) : (confirm = { kind: 'disable', p })) })
    out.push({ label: 'Remove', danger: true, run: () => (confirm = { kind: 'remove', p }) })
    return out
  }

  // Confirm text names what else changes: the default moves, or nothing is
  // left enabled.
  let confirmText = $derived.by(() => {
    if (!confirm) return null
    const p = confirm.p
    const last = !p.disabled && enabled.length === 1
    const isDefault = list?.default === p.name
    if (confirm.kind === 'default') {
      return { title: `Make ${p.name} the default?`, lines: [`New sessions and workflow agents that name no profile will use ${p.name}${list?.default ? ` instead of ${list.default}` : ''}.`], action: 'Make default', danger: false }
    }
    const lines: string[] = []
    if (confirm.kind === 'remove') lines.push(`This deletes ${p.name} from your profiles. Sessions that already use it keep running.`)
    else lines.push(`Workflows can no longer use ${p.name}, and / no longer offers it. Its settings stay saved.`)
    if (isDefault) lines.push(`${p.name} is your default profile. Dyna will fall back to the first enabled profile.`)
    if (last) lines.push(`It is your only enabled profile. Nothing can start a session until you enable or add one.`)
    return { title: confirm.kind === 'remove' ? `Remove ${p.name}?` : `Disable ${p.name}?`, lines, action: confirm.kind === 'remove' ? 'Remove profile' : 'Disable profile', danger: true }
  })

  async function act(kind: ConfirmKind, p: ProfileView) {
    if (kind === 'default') await api.setDefaultProfile(p.name)
    else if (kind === 'disable') await api.setProfileDisabled(p.name, true)
    else if (kind === 'enable') await api.setProfileDisabled(p.name, false)
    else if (kind === 'remove') await api.removeProfile(p.name)
    await load()
    say({ default: `${p.name} is now the default.`, disable: `${p.name} is disabled.`, enable: `${p.name} is enabled.`, remove: `${p.name} was removed.` }[kind])
  }

  async function runConfirm() {
    const asked = confirm
    if (!asked) return
    const { kind, p } = asked
    confirm = { ...asked, busy: true, error: '' }
    try {
      await act(kind, p)
      confirm = null
    } catch (e) {
      confirm = { ...asked, busy: false, error: errorText(e) }
    }
  }

  // One checkbox in the uses grid saves that profile through the editor's
  // save. Its row stays locked until the save and the reload finish, so the
  // next change starts from the saved uses. A save error outlives the reload.
  let saving = $state<string[]>([]) // names of profiles whose uses are saving
  async function setUses(p: ProfileView, uses: string[]) {
    saving = [...saving, p.name]
    let failed = ''
    try {
      await api.saveProfile({ original: p.name, profile: { ...$state.snapshot(p), uses } })
    } catch (e) {
      failed = errorText(e)
    }
    await load()
    if (failed) error = failed
    saving = saving.filter((n) => n !== p.name)
  }

  // A test updates the row's chip without reloading the list.
  function checked(c: ProfileCheck) {
    if (!list || !c?.support) return
    list.profiles = list.profiles.map((p) => (p.name === c.name ? { ...p, support: c.support } : p))
  }

  async function saved(v: ProfileView) {
    editing = null
    creating = false
    await load()
    say(`Saved ${v.name}.`)
  }

  onMount(() => { load(); loadUsage() })
</script>

<section>
  <TabHeader flush title="Profiles" blurb="A profile is a harness and a model that sessions and workflow agents run on. Type /name in a room to start a session with one.">
    {#snippet action()}<button class="btn fill" onclick={() => { wizardSelection = null; creating = true }}>New profile</button>{/snippet}
  </TabHeader>
  {#if error}<p class="err">{error}</p>{/if}
  {#if note}<p class="note" role="status">{note}</p>{/if}
  {#if !list && !error}<Skeleton label="Loading profiles" rows={3} />{/if}
  {#if list && !list.profiles.length}
    <EmptyState title="No profiles yet">A profile is a harness and a model. Add one with New profile above, or run <span class="mono">dyna profiles add</span> in a terminal.</EmptyState>
  {/if}
  {#if list}
    {#if list.profiles.length}
      <div class="filter-bar">
        <label class="profile-filter">Filter profiles
          <input type="search" bind:this={filterInput} bind:value={filterQuery} placeholder="Name, harness, model, or description" aria-controls="profile-cards" />
        </label>
        <button class="btn quiet" aria-label="Clear profile filter" disabled={!filterQuery} onclick={() => { filterQuery = ''; filterInput?.focus() }}>Clear</button>
        <span class="count" role="status">{filteredProfiles.length} of {list.profiles.length} profiles</span>
        <label class="profile-window">Activity window
          <select bind:value={usagePeriod} onchange={loadUsage} aria-label="Measured activity window">
            {#each usagePeriods as option (option.value)}<option value={option.value}>{option.label}</option>{/each}
          </select>
        </label>
      </div>
      <p class="measurement-note">Measured activity uses UTC days. Cost per turn uses reported turns; ≥ marks a lower bound. Authored ratings stay unchanged.</p>
      {#if usageError}<p class="err">Measured activity unavailable: {usageError}</p>{/if}
      {#if !filteredProfiles.length}
        <EmptyState title="No matching profiles">Try another name, harness, model, or description, or clear the filter.</EmptyState>
      {/if}
    {/if}
    <div class="list" id="profile-cards">
      {#each filteredProfiles as p (p.name)}
        {@const h = healthChip(p)}
        {@const measured = usageByName.get(p.name)}
        <div class="prof" class:off={p.disabled} transition:slide={{ duration: motion(BASE) }}>
          <span class="glyph bg-{harnessHue[p.harness] ?? 0}" aria-hidden="true">{short[p.harness] ?? '?'}</span>
          <div class="main">
            <div class="name">
              <span>{p.name}</span>
              {#if list.default === p.name}<span class="badge def">default</span>{/if}
              {#if p.disabled}<span class="badge">disabled</span>{/if}
              {#if p.safeMode}<span class="badge">safe mode</span>{/if}
              {#if h}<span class="badge {h.tone}" title={h.title}>{h.text}</span>{/if}
            </div>
            <div class="sub" title={(p.models?.length ?? 0) > 1 ? (p.models ?? []).join(' → ') : undefined}>{p.harness}{p.model ? ' · ' + p.model : ''}{#if (p.models?.length ?? 0) > 1}&nbsp;→ +{(p.models?.length ?? 0) - 1} fallback{/if}{#if p.envKeys?.length}&nbsp;· {p.envKeys.length} env {p.envKeys.length === 1 ? 'value' : 'values'}{/if}</div>
            {#if p.description}<div class="desc">{p.description}</div>{/if}
          </div>
          <div class="stats" aria-label="taste {p.taste}, diagnostics {p.diagnostics}, intelligence {p.intelligence}, cost-efficiency {p.cost} out of 10">
            {#each ([['taste', p.taste], ['diagnostics', p.diagnostics], ['smarts', p.intelligence], ['thrift', p.cost]] as const) as [label, v]}
              <div class="stat"><span>{label}</span><span class="bar"><span style="width:{v * 10}%"></span></span></div>
            {/each}
          </div>
          <dl class="measured" aria-label="Measured activity for {p.name}">
            {#if measured}
              <div><dt>turns</dt><dd>{measured.turns.toLocaleString('en-US')}</dd></div>
              <div><dt>median turn</dt><dd>{medianText(measured.medianWallMs)}</dd></div>
              <div title={costPerTurnTitle(measured)}><dt>cost / reported turn</dt><dd>{costPerTurnText(measured)}</dd></div>
              <div title="Cache share uses {measured.cachePaired.turns} turns that reported both input and cache."><dt>cache read</dt><dd>{cacheText(measured)}</dd></div>
              <div title={spendTitle(measured)}><dt>total spend</dt><dd>{costText(measured.cost)}</dd></div>
            {:else}
              <div class="measured-empty"><dt>activity</dt><dd>{usageError ? 'unavailable' : usage ? 'no turns' : 'loading…'}</dd></div>
            {/if}
          </dl>
          <button class="more" aria-label="Actions for {p.name}" onclick={(e) => { const r = e.currentTarget.getBoundingClientRect(); menu = { p, x: r.right - 210, y: r.bottom + 4 } }}>
            <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><circle cx="5" cy="10" r="1.6" fill="currentColor" /><circle cx="10" cy="10" r="1.6" fill="currentColor" /><circle cx="15" cy="10" r="1.6" fill="currentColor" /></svg>
          </button>
        </div>
      {/each}
    </div>
    <div class="usegrid">
      <h3>Who may use each profile</h3>
      <p class="use-scope">All profiles, including those hidden by the filter.</p>
      <ProfileUses profiles={list.profiles} busy={saving} onchange={setUses} />
    </div>
  {/if}
</section>

{#if menu}
  <Menu x={menu.x} y={menu.y} title={menu.p.name} items={items(menu.p)} onclose={() => (menu = null)} />
{/if}
{#if confirm && confirmText}
  <Confirm {...confirmText} busy={confirm.busy} error={confirm.error} onconfirm={runConfirm} oncancel={() => (confirm = null)} />
{/if}
{#if creating}
  {#key wizardSelection}
  <ProfileWizard harnesses={list?.harnesses ?? []} unready={list?.unready ?? {}} initialSelection={wizardSelection} {onusage} onsaved={saved} oncancel={() => (creating = false)} />
  {/key}
{/if}
{#if asking}
  <SupportBrief profile={asking} profiles={list?.profiles ?? []} {roomId} onstarted={(by) => { asking = null; say(`Started a support session on ${by}.`) }} oncancel={() => (asking = null)} />
{/if}
{#if editing}
  <ProfileEditor original={editing.original} profile={editing.profile} harnesses={list?.harnesses ?? []} profiles={list?.profiles ?? []} onasksupport={(p) => (asking = p)} onchecked={checked} onsaved={saved} oncancel={() => (editing = null)} />
{/if}

<style>
  .filter-bar { display: flex; flex-wrap: wrap; align-items: end; gap: var(--s2); margin-bottom: var(--s4); }
  .profile-filter { display: flex; flex: 1; flex-direction: column; gap: var(--s1); min-width: 180px; font-size: var(--t-sm); color: var(--muted); }
  .profile-filter input { box-sizing: border-box; width: 100%; min-width: 0; min-height: 36px; padding: 7px 10px; font: inherit; font-size: var(--t-ui); color: var(--ink); background: var(--side); border: 1px solid var(--line); border-radius: var(--r-xs); }
  .profile-window { display: flex; flex-direction: column; gap: var(--s1); color: var(--muted); font-size: var(--t-sm); }
  .profile-window select { min-height: 36px; padding: 7px 28px 7px 10px; color: var(--ink); background: var(--side); border: 1px solid var(--line); border-radius: var(--r-xs); font: inherit; font-size: var(--t-ui); }
  .measurement-note { margin: 0 0 var(--s3); color: var(--faint); font-size: var(--t-sm); }
  .filter-bar .btn { min-height: 36px; }
  .count { align-self: end; padding-bottom: 9px; color: var(--faint); font-size: var(--t-sm); white-space: nowrap; }
  .use-scope { margin: 0 0 var(--s3); color: var(--faint); font-size: var(--t-sm); }
  .usegrid { margin-top: var(--s5); }
  .usegrid h3 { margin: 0 0 var(--s2); font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .err { color: var(--coral); }
  .note { color: var(--sage); font-size: var(--t-ui); margin: 0 0 10px; }
  .list { display: flex; flex-direction: column; gap: var(--s2); }
  .prof { display: grid; grid-template-columns: 40px minmax(0, 1fr) auto 32px; grid-template-areas: 'glyph main main more' '. stats measured .'; align-items: center; gap: 14px; background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; padding: 14px var(--s3) 14px var(--s4); transition: border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .prof:hover { border-color: var(--line); }
  }
  .prof.off { opacity: 0.55; }
  .glyph { grid-area: glyph; width: 40px; height: 40px; border-radius: 12px; display: grid; place-items: center; font-size: var(--t-ui); font-weight: 700; color: #17161a; flex: none; background: var(--h); }
  .main { grid-area: main; min-width: 0; }
  .name { display: flex; align-items: center; gap: var(--s2); font-weight: 600; font-size: var(--t-lead); }
  .badge { font-size: var(--t-cap); font-weight: 600; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 1px 6px; }
  .badge.bad { color: var(--coral); background: rgba(239, 138, 128, 0.1); }
  .badge.warn { color: var(--gold); background: var(--gold-wash); }
  .badge.def { color: #18161d; background: var(--gold); }
  .sub { font-size: var(--t-ui); color: var(--muted); margin-top: 2px; }
  .desc { font-size: var(--t-ui); color: var(--faint); margin-top: 3px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .stats { grid-area: stats; display: flex; flex-direction: column; justify-self: end; gap: var(--s1); width: 150px; flex: none; }
  .measured { grid-area: measured; display: grid; grid-template-columns: repeat(3, minmax(62px, 1fr)); gap: var(--s2) var(--s3); width: 240px; flex: none; margin: 0; }
  .measured div { min-width: 0; }
  .measured dt { color: var(--faint); font-size: var(--t-cap); }
  .measured dd { margin: 1px 0 0; color: var(--ink-2); font-size: var(--t-ui); font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .measured .measured-empty { grid-column: 1 / -1; align-self: center; }
  .measured-empty dd { color: var(--faint); font-weight: 400; }
  .stat { display: grid; grid-template-columns: 72px 1fr; align-items: center; gap: var(--s2); font-size: var(--t-cap); color: var(--faint); }
  .bar { height: 5px; background: var(--rail); border-radius: 3px; overflow: hidden; }
  .bar span { display: block; height: 100%; background: var(--iris); border-radius: 3px; opacity: 0.85; }
  .more { grid-area: more; width: 32px; height: 32px; border-radius: 6px; color: var(--muted); display: grid; place-items: center; flex: none; transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .more:hover { background: var(--hover); color: var(--ink); }
  }
  .more:active { background: var(--select); }

  /* A narrow pane gives the identity and both evidence groups full rows. */
  @media (max-width: 1100px) {
    .prof { grid-template-columns: 40px minmax(0, 1fr) 32px; grid-template-areas: 'glyph main more' 'stats stats stats' 'measured measured measured'; }
    .stats, .measured { justify-self: stretch; width: auto; }
  }

  /* A phone: the meters move under the name as one row of three, so the
     name, model and badges get the full width and wrap instead of
     squeezing. The menu button stays at the top right, 44 px. */
  @media (max-width: 760px) {
    .btn, .filter-bar .btn, .profile-filter input, .profile-window select { min-height: 44px; }
    .count { padding-bottom: 13px; }
    .prof { display: grid; grid-template-columns: 40px minmax(0, 1fr) 44px; grid-template-areas: 'glyph main more' 'stats stats stats' 'measured measured measured'; column-gap: var(--s3); row-gap: var(--s3); align-items: start; padding: var(--s3) 6px 14px 14px; }
    .glyph { grid-area: glyph; }
    .main { grid-area: main; }
    .more { grid-area: more; width: 44px; height: 44px; margin-top: -4px; }
    .name { flex-wrap: wrap; row-gap: var(--s1); }
    .desc { white-space: normal; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; }
    .stats { grid-area: stats; width: auto; flex-direction: row; gap: var(--s3); padding-right: var(--s2); }
    .measured { grid-area: measured; width: auto; padding-right: var(--s2); }
    .stat { flex: 1; grid-template-columns: 1fr; gap: var(--s1); }
  }
</style>
