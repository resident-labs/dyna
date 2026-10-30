<script lang="ts">
  // The hooks of one scope: a repository's, or the ones that belong to this
  // computer. Each row says when the hook runs and how its last run went, with
  // its switch on the right; a click shows its command, its actions and its
  // runs, and a run opens its log. `internal/hooks` does the work, the same code
  // `dyna git hooks` uses. A hook that does a service's work (hide) shows under
  // that service instead: there the list renders only that hook (only), open.
  import { errorText } from '../errorText.ts'
  import { onMount, tick } from 'svelte'
  import { slide } from 'svelte/transition'
  import { api } from '../../api.ts'
  import Card from '../ui/Card.svelte'
  import SettingRow from '../ui/SettingRow.svelte'
  import StatusPill from '../ui/StatusPill.svelte'
  import Switch from '../ui/Switch.svelte'
  import Skeleton from '../ui/Skeleton.svelte'
  import InlineConfirm from '../ui/InlineConfirm.svelte'
  import { BASE, motion } from '../ui/motion.ts'
  import { ago, duration } from '../ui/format.ts'
  import HookForm from '../settings/HookForm.svelte'
  import { eventLabel, exitLabel, hookForm, hookOf, hookPatch, authorLabel, newHook, newestFirst, timeoutLabel, type HookFields, type HookScope } from '../settings/hooks.ts'
  import type { HookLogTarget } from '../settings/hookLog.ts'
  import type { Hook, HookRun, HookRunLog, HookSummary } from '../api/settings.ts'
  import { when } from './model.ts'

  // A hook the owner asked about removing, inline.
  interface Removing {
    name: string
    busy?: boolean
    error?: string
  }
  // The add or edit form: the hook (null when adding), its form values and save state.
  interface Editor {
    hook: Hook | null
    form: HookFields
    busy?: boolean
    error?: string
  }

  let { title, repo = '', scope = 'repository', focus = null, hide = [], only = '' }: { title: string; repo?: string; scope?: HookScope; focus?: HookLogTarget | null; hide?: string[]; only?: string } = $props()

  // A test runs these functions without props, so the check tolerates no scope.
  const isGlobal = () => typeof scope !== 'undefined' && scope === 'global'

  let hooks = $state<Hook[] | null>(null)
  let runs = $state<Record<string, HookRun[]>>({}) // hook name -> runs, newest first
  let logs = $state<Record<string, HookRunLog>>({}) // run id -> {tail, exitCode}
  let next = $state<Record<string, string>>({}) // hook name -> next scheduled run
  let busy = $state<Record<string, boolean>>({}) // hook name -> true while its row waits
  let open = $state('') // hook name whose runs are showing
  let openRun = $state('') // run id whose log is showing
  let removing = $state<Removing | null>(null)
  let editor = $state<Editor | null>(null)
  let error = $state('')
  let note = $state('')
  // Rows that arrive with the first load appear at once; later ones slide.
  let loading = $state(true)
  const shown = 10 // runs listed per hook
  const listed = $derived((hooks ?? []).filter((h) => (only ? h.name === only : !hide.includes(h.name))))

  function say(text: string) {
    note = text
    setTimeout(() => { if (note === text) note = '' }, 2600)
  }

  async function loadRuns(name: string) {
    runs[name] = newestFirst<HookRun>(await (isGlobal() ? api.globalHookRuns(name) : api.hookRuns(repo, name)))
  }

  async function loadLog(name: string, id: string) {
    try {
      logs[id] = await (isGlobal() ? api.globalHookRunLog(name, id) : api.hookRunLog(repo, name, id))
    } catch (e) {
      logs[id] = { tail: errorText(e), exitCode: -1 }
    }
  }

  // A reload after a change keeps the list on screen, so rows slide in and out.
  async function load(quiet = false) {
    error = ''
    if (!quiet) {
      hooks = null
      runs = {}
      next = {}
      open = ''
      openRun = ''
    }
    try {
      const listed: Hook[] = (await (isGlobal() ? api.listGlobalHooks() : api.listHooks(repo))) ?? []
      hooks = listed
      await Promise.all(listed.map((h) => loadRuns(h.name)))
      // The next run is a helpful extra: a failed read leaves the rows on
      // screen instead of hiding every hook.
      try {
        const summaries: HookSummary[] = (await (isGlobal() ? api.globalHookSummary() : api.hookSummary(repo))) ?? []
        next = Object.fromEntries(summaries.flatMap((s) => (s.nextRun ? [[s.hook.name, s.nextRun]] : [])))
      } catch {
        next = {}
      }
    } catch (e) {
      hooks = []
      error = errorText(e)
    }
  }

  async function run(h: Hook) {
    busy[h.name] = true
    error = ''
    try {
      const result = await (isGlobal() ? api.runGlobalHook(h.name) : api.runHook(repo, h.name))
      say(`${h.label || h.name} ${result?.result === 'pending' ? 'started' : 'passed'}.`)
    } catch (e) {
      // A failing hook is a result, not a fault. The new run shows below.
      say(`${h.label || h.name} did not pass.`)
    }
    try {
      await loadRuns(h.name)
      open = h.name
    } catch (e) {
      error = errorText(e)
    }
    busy[h.name] = false
  }

  // toggle turns the automatic runs of one hook on or off, and keeps its
  // settings.
  async function toggle(h: Hook, on: boolean) {
    busy[h.name] = true
    error = ''
    try {
      await (isGlobal() ? api.editGlobalHook(h.name, { disabled: !on }) : api.editHook(repo, h.name, { disabled: !on }))
      await load(true)
    } catch (e) {
      error = errorText(e)
    }
    busy[h.name] = false
  }

  async function remove() {
    const asked = removing
    if (!asked) return
    removing = { ...asked, busy: true, error: '' }
    try {
      await (isGlobal() ? api.removeGlobalHook(asked.name) : api.removeHook(repo, asked.name))
      if (removing?.name !== asked.name) return
      removing = null
      say(`${asked.name} was removed.`)
      await load(true)
    } catch (e) {
      if (removing?.name === asked.name) removing = { ...removing, busy: false, error: errorText(e) }
    }
  }

  async function saveHook() {
    const current = editor
    if (!current) return
    const { hook, form } = current
    editor = { ...current, busy: true, error: '' }
    try {
      if (!hook) {
        await (isGlobal() ? api.addGlobalHook(hookOf(form)) : api.addHook(repo, hookOf(form)))
      } else {
        const patch = hookPatch(hook, form)
        if (Object.keys(patch).length) await (isGlobal() ? api.editGlobalHook(hook.name, patch) : api.editHook(repo, hook.name, patch))
      }
      editor = null
      say(`${form.name.trim() || hook?.name} was ${hook ? 'saved' : 'added'}.`)
      await load(true)
    } catch (e) {
      editor = { ...current, busy: false, error: errorText(e) }
    }
  }

  function toggleRuns(name: string) {
    open = open === name ? '' : name
    openRun = ''
  }

  function toggleLog(name: string, id: string) {
    openRun = openRun === id ? '' : id
    if (openRun && !logs[id]) loadLog(name, id)
  }

  // A hook card asks for one run. The list opens its hook and its log, so the
  // card's "View log" lands on the run that posted it. The card names the
  // repository as the hook engine recorded it, and a room may hold another
  // form of the same path, so the hooks loaded for this repository decide.
  async function showFocus(target: HookLogTarget) {
    if (!hooks?.some((h) => h.name === target.hook)) {
      error = `No hook named ${target.hook} is on this repository, so that run of ${target.repo} cannot show here.`
      return
    }
    try {
      open = target.hook
      openRun = target.runId
      await loadRuns(target.hook)
      if (!logs[target.runId]) await loadLog(target.hook, target.runId)
    } catch (e) {
      error = errorText(e)
    }
  }

  onMount(async () => {
    await load()
    await tick()
    loading = false
    if (focus) await showFocus(focus)
  })

  const failed = (r: HookRun | undefined): boolean => r?.result === 'error' || r?.result === 'fail'
</script>

{#snippet runList(h: Hook)}
  <div class="more">
    <pre class="cmd mono">{h.command}</pre>
    <p class="facts">Runs in <span class="mono">{h.cwd}</span> · time limit {timeoutLabel(h.timeout)} · added by {authorLabel(h.addedBy)}{h.addedAt ? ` · ${ago(h.addedAt)}` : ''}</p>
    <div class="acts">
      {#if removing?.name === h.name}
        <InlineConfirm busy={removing.busy} onconfirm={remove} oncancel={() => (removing = null)} />
      {:else}
        <button class="btn" disabled={busy[h.name]} onclick={() => run(h)}>{busy[h.name] ? 'Running…' : 'Run now'}</button>
        <button class="btn quiet" disabled={busy[h.name] || !!editor} onclick={() => (editor = { hook: h, form: hookForm(h) })}>Edit</button>
        <button class="btn quiet" disabled={busy[h.name]} onclick={() => (removing = { name: h.name })}>Remove</button>
      {/if}
    </div>
    {#each (runs[h.name] ?? []).slice(0, shown) as r (r.id)}
      <button class="run" aria-expanded={openRun === r.id} onclick={() => toggleLog(h.name, r.id)}>
        <span class="dot" class:bad={failed(r)} aria-hidden="true"></span>
        <span class="res">{r.result}</span>
        <span>{ago(r.startedAt)}</span>
        <span>{duration(r.startedAt, r.finishedAt)}</span>
        {#if exitLabel(logs[r.id]?.exitCode)}<span class="mono">{exitLabel(logs[r.id]?.exitCode)}</span>{/if}
      </button>
      {#if openRun === r.id}<pre class="log mono">{logs[r.id]?.tail || (logs[r.id] ? '(empty log)' : 'Reading the log…')}</pre>{/if}
    {:else}
      <p class="facts">Never run.</p>
    {/each}
  </div>
{/snippet}

{#snippet hookRow(h: Hook)}
  {@const last = runs[h.name]?.[0]}
  {#snippet more()}{@render runList(h)}{/snippet}
  <div transition:slide={{ duration: loading ? 0 : motion(BASE) }}>
    {#if editor?.hook?.name === h.name}
      <div class="form"><HookForm bind:form={editor.form} editing busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} /></div>
    {:else}
      <SettingRow inline title={h.label || h.name} description={eventLabel(h.event, h.everyMinutes)} expanded={open === h.name} ontoggle={() => toggleRuns(h.name)} children={open === h.name ? more : undefined}>
        {#snippet status()}
          {@const nextAt = next[h.name]}
          {@const facts = [last && !failed(last) ? `Last ${when(last.startedAt, Date.now())}` : '', nextAt && !h.disabled ? `Next ${when(nextAt, Date.now())}` : ''].filter(Boolean)}
          {#if last && failed(last)}<StatusPill label="Failed {when(last.startedAt, Date.now())}" tone="danger" />{/if}
          {#if facts.length}<span>{facts.join(' · ')}</span>{/if}
          {#if h.onFail === 'block'}<StatusPill label="Blocks landing" />{/if}
          {#if removing?.name === h.name && removing.error}<span class="err" role="alert">{removing.error}</span>{/if}
        {/snippet}
        {#snippet control()}
          <Switch on={!h.disabled} label={`${h.label || h.name} runs automatically`} disabled={busy[h.name] || !!editor} onchange={(on: boolean) => toggle(h, on)} />
        {/snippet}
      </SettingRow>
    {/if}
  </div>
{/snippet}

{#if only}
  {#each listed as h (h.name)}
    {#if editor?.hook?.name === h.name}
      <HookForm bind:form={editor.form} editing busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} />
    {:else}
      {@render runList(h)}
    {/if}
  {/each}
  {#if error}<p class="facts err" role="alert">{error}</p>{/if}
  {#if note}<p class="facts" role="status">{note}</p>{/if}
{:else}
<Card {title} flush divided count={listed.length || null}>
  {#snippet action()}{#if hooks && !editor}<button class="btn quiet" onclick={() => (editor = { hook: null, form: newHook() })}>Add hook</button>{/if}{/snippet}
  {#if editor && !editor.hook}
    <div class="form"><HookForm form={editor.form} busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} /></div>
  {/if}
  {#if hooks === null && !error}<Skeleton label="Loading hooks" rows={2} />{/if}
  {#each listed as h (h.name)}{@render hookRow(h)}{/each}
  {#if hooks && !listed.length && !editor}
    <p class="line">No hooks yet. {isGlobal() ? 'A hook here runs for the whole computer.' : 'A hook here runs for every room on this repository.'}</p>
  {/if}
  {#if error}<p class="line err" role="alert">{error}</p>{/if}
  {#if note}<p class="line" role="status">{note}</p>{/if}
</Card>
{/if}

<style>
  .form { padding: var(--s3) var(--s4); }
  .line { margin: 0; padding: var(--s3) var(--s4); font-size: var(--t-sm); color: var(--muted); }
  .err { color: var(--tone-danger-fg); }
  .more { display: flex; flex-direction: column; gap: var(--s1); }
  .acts { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin-bottom: var(--s2); }
  .facts { margin: 0 0 var(--s2); font-size: var(--t-sm); color: var(--muted); }
  /* A command wraps at its spaces and scrolls when one word is longer than the row. */
  .cmd { margin: 0 0 var(--s1); padding: var(--s2) var(--s3); background: var(--rail); border-radius: var(--r-xs); white-space: pre-wrap; overflow-x: auto; font-size: var(--t-sm); color: var(--ink); }
  .run { display: flex; align-items: center; gap: var(--s3); width: 100%; padding: var(--s1) var(--s2); border-radius: var(--r-xs); text-align: left; font-size: var(--t-sm); color: var(--muted); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .run:hover { background: var(--hover); }
  }
  .run:active { background: var(--select); }
  .res { color: var(--ink-2); }
  .dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--tone-neutral); }
  .dot.bad { background: var(--tone-danger); }
  .log { margin: 0 0 var(--s1); padding: var(--s2) var(--s3); max-height: 16rem; overflow: auto; white-space: pre-wrap; word-break: break-all; background: var(--rail); border-radius: var(--r-xs); font-size: var(--t-sm); color: var(--muted); }
  @media (max-width: 760px) {
    .run { min-height: 44px; flex-wrap: wrap; }
  }
</style>
