<script lang="ts">
  // Repo hooks: commands that run on repo events. This pane lists the hooks of
  // one repository, shows the last run, and adds, edits, runs or removes a
  // hook. `internal/hooks` does the work, the same code `dyna git hooks` uses.
  // Room settings hosts the pane with the room's repository, and hooks belong
  // to the repository, so a change here reaches every room on it.
  import { errorText } from '../errorText.ts'
  import { onMount } from 'svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { api } from '../../api.ts'
  import Skeleton from '../ui/Skeleton.svelte'
  import InlineConfirm from '../ui/InlineConfirm.svelte'
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../ui/motion.ts'
  import { ago, duration } from '../ui/format.ts'
  import HookForm from './HookForm.svelte'
  import { eventLabel, exitLabel, failLabel, hookForm, hookOf, hookPatch, newHook, newestFirst, runTone, timeoutLabel, type HookFields } from './hooks.ts'
  import type { HookLogTarget } from './hookLog.ts'
  import type { Hook, HookRun, HookRunLog } from '../api/settings.ts'

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

  let { onmodal = null, repo, focus = null }: { onmodal?: ((open: boolean) => void) | null; repo: string; focus?: HookLogTarget | null } = $props()

  let hooks = $state<Hook[] | null>(null)
  let runs = $state<Record<string, HookRun[]>>({}) // hook name -> runs, newest first
  let logs = $state<Record<string, HookRunLog>>({}) // run id -> {tail, exitCode}
  let open = $state('') // hook name whose runs are showing
  let openRun = $state('') // run id whose log is showing
  let running = $state<Record<string, boolean>>({}) // hook name -> true while a run is in flight
  let removing = $state<Removing | null>(null) // the hook asked about inline: {name, busy, error}
  let editor = $state<Editor | null>(null) // {hook (null when adding), form, busy, error}
  let error = $state('')
  let note = $state('')
  const shown = 10 // runs listed per hook

  // An open form keeps Esc from closing Settings over unsaved input.
  // Unmounting with an editor open (another settings section) frees Esc.
  $effect(() => { onmodal?.(!!editor); return () => onmodal?.(false) })

  function say(text: string) {
    note = text
    setTimeout(() => { if (note === text) note = '' }, 2600)
  }

  async function loadRuns(name: string) {
    const list = newestFirst<HookRun>(await api.hookRuns(repo, name))
    runs[name] = list
    if (list[0] && !logs[list[0].id]) await loadLog(name, list[0].id)
  }

  async function loadLog(name: string, id: string) {
    try {
      logs[id] = await api.hookRunLog(repo, name, id)
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
      open = ''
      openRun = ''
    }
    try {
      const listed: Hook[] = (await api.listHooks(repo)) ?? []
      hooks = listed
      await Promise.all(listed.map((h) => loadRuns(h.name)))
    } catch (e) {
      hooks = []
      error = errorText(e)
    }
  }

  async function run(h: Hook) {
    running[h.name] = true
    error = ''
    try {
      const result = await api.runHook(repo, h.name)
      say(`${h.label || h.name} ${result?.result === 'pending' ? 'started' : 'passed'}.`)
    } catch (e) {
      // A failing command is a result, not a fault. The new run shows below.
      say(`${h.name} did not pass.`)
    }
    try {
      await loadRuns(h.name)
      open = h.name
      for (const r of (runs[h.name] ?? []).slice(0, shown)) if (!logs[r.id]) loadLog(h.name, r.id)
    } catch (e) {
      error = errorText(e)
    }
    running[h.name] = false
  }

  async function remove() {
    const asked = removing
    if (!asked) return
    removing = { ...asked, busy: true, error: '' }
    try {
      await api.removeHook(repo, asked.name)
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
      if (!hook) await api.addHook(repo, hookOf(form))
      else {
        const patch = hookPatch(hook, form)
        if (Object.keys(patch).length) await api.editHook(repo, hook.name, patch)
      }
      editor = null
      say(`${form.name.trim()} was ${hook ? 'saved' : 'added'}.`)
      await load(true)
    } catch (e) {
      editor = { ...current, busy: false, error: errorText(e) }
    }
  }

  function toggleRuns(name: string) {
    open = open === name ? '' : name
    openRun = ''
    if (open) for (const r of (runs[name] ?? []).slice(0, shown)) if (!logs[r.id]) loadLog(name, r.id)
  }

  function toggleLog(name: string, id: string) {
    openRun = openRun === id ? '' : id
    if (openRun && !logs[id]) loadLog(name, id)
  }

  // A hook card asks for one run. The pane opens its hook and its log, so the
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
    if (focus) await showFocus(focus)
  })
</script>

<section>
  {#if error}<p class="err">{error}</p>{/if}
  {#if note}<p class="note" role="status">{note}</p>{/if}

  {#if hooks && !editor}
    <div class="tools"><button class="btn" onclick={() => (editor = { hook: null, form: newHook() })}>+ Add hook</button></div>
  {/if}
  {#if editor && !editor.hook}
    <div class="adding"><HookForm bind:form={editor.form} busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} /></div>
  {/if}

  {#if hooks === null && !error}<Skeleton label="Loading hooks" rows={3} />{/if}

  {#if hooks && !hooks.length && !error && !editor}
    <EmptyState title="No hooks yet">Add one here, or from a terminal in the repo: <code class="mono">dyna git hooks add verify --event before-land --command 'go test ./...' --on-fail block</code></EmptyState>
  {/if}

  {#if hooks}
    <div class="list">
      {#each hooks as h (h.name)}
        {@const list = runs[h.name] ?? []}
        {@const last = list[0]}
        {#if editor?.hook?.name === h.name}
        <HookForm bind:form={editor.form} editing busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} />
        {:else}
        <div class="hook" transition:slide={{ duration: motion(BASE) }}>
          <div class="top">
            <span class="name">{h.label || h.name}</span>
            <span class="badge">{eventLabel(h.event, h.everyMinutes)}</span>
            <span class="badge" class:block={h.onFail === 'block'} title={h.onFail === 'block' ? 'A failure stops the land' : 'A failure sends an alert'}>{failLabel(h.onFail)}</span>
            <span class="gap"></span>
            <button class="btn" disabled={running[h.name] || !!editor} onclick={() => (editor = { hook: h, form: hookForm(h) })}>Edit</button>
            <button class="btn" disabled={running[h.name]} onclick={() => run(h)}>{running[h.name] ? 'Running…' : 'Run now'}</button>
            {#if removing?.name === h.name}
              <InlineConfirm busy={removing.busy} onconfirm={remove} oncancel={() => (removing = null)} />
            {:else}
              <button class="btn quiet" disabled={running[h.name]} onclick={() => (removing = { name: h.name })}>Remove</button>
            {/if}
          </div>
          {#if removing?.name === h.name && removing.error}<p class="err" role="alert">{removing.error}</p>{/if}
          <pre class="cmd mono">{h.command}</pre>
          <div class="meta">
            <span>cwd <b class="mono">{h.cwd}</b></span>
            <span>timeout <b>{timeoutLabel(h.timeout)}</b></span>
            <span>added by <b>{h.addedBy || 'unknown'}</b>{h.addedAt ? ' · ' + ago(h.addedAt) : ''}</span>
          </div>
          {#if last}
            <button class="last" aria-expanded={open === h.name} onclick={() => toggleRuns(h.name)}>
              <span class="dot {runTone(last.result)}" aria-hidden="true"></span>
              <span class="res">{last.result}</span>
              <span>{ago(last.startedAt)}</span>
              <span>{duration(last.startedAt, last.finishedAt)}</span>
              {#if exitLabel(logs[last.id]?.exitCode)}<span class="mono">{exitLabel(logs[last.id]?.exitCode)}</span>{/if}
              <span class="chev" aria-hidden="true">{open === h.name ? '▾' : '▸'}</span>
            </button>
          {:else}
            <div class="last none"><span class="dot idle" aria-hidden="true"></span><span>Never run</span></div>
          {/if}
          {#if open === h.name}
            <div class="runs">
              {#each list.slice(0, shown) as r (r.id)}
                <button class="run" aria-expanded={openRun === r.id} onclick={() => toggleLog(h.name, r.id)}>
                  <span class="dot {runTone(r.result)}" aria-hidden="true"></span>
                  <span class="res">{r.result}</span>
                  <span>{ago(r.startedAt)}</span>
                  <span>{duration(r.startedAt, r.finishedAt)}</span>
                  {#if exitLabel(logs[r.id]?.exitCode)}<span class="mono">{exitLabel(logs[r.id]?.exitCode)}</span>{/if}
                </button>
                {#if openRun === r.id}
                  <pre class="log mono">{logs[r.id]?.tail || (logs[r.id] ? '(empty log)' : 'Loading…')}</pre>
                {/if}
              {/each}
            </div>
          {/if}
        </div>
        {/if}
      {/each}
    </div>
  {/if}
</section>


<style>
  .err { color: var(--coral); }
  .note { color: var(--sage); font-size: var(--t-ui); margin: 0 0 10px; }
  .tools { display: flex; justify-content: flex-end; margin: 0 0 10px; }
  .adding { margin: 0 0 10px; }
  .list { display: flex; flex-direction: column; gap: 10px; }
  .hook { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; padding: 14px var(--s4); transition: border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .hook:hover { border-color: var(--line); }
  }
  .top { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; }
  .name { font-weight: 600; font-size: var(--t-lead); }
  .gap { flex: 1; }
  .badge { font-size: var(--t-cap); font-weight: 600; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 1px 6px; }
  .badge.block { color: var(--gold); background: var(--gold-wash); }
  .cmd { margin: 10px 0 var(--s2); padding: var(--s2) 10px; background: var(--rail); border-radius: 6px; white-space: pre-wrap; word-break: break-all; color: var(--ink); }
  .meta { display: flex; flex-wrap: wrap; gap: var(--s1) var(--s4); font-size: var(--t-sm); color: var(--faint); }
  .meta b { color: var(--muted); font-weight: 500; }
  .last, .run { display: flex; align-items: center; gap: 10px; font-size: var(--t-ui); color: var(--muted); width: 100%; text-align: left; border-radius: 6px; padding: 6px var(--s2); transition: background-color var(--motion-fast) var(--ease); }
  .last { margin-top: 10px; background: var(--rail); }
  button.last { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    button.last:hover, .run:hover { background: var(--hover); }
  }
  button.last:active, .run:active { background: var(--select); }
  .last.none { color: var(--faint); }
  .chev { margin-left: auto; color: var(--faint); }
  .res { color: var(--ink); font-weight: 600; }
  .dot { width: 9px; height: 9px; border-radius: 50%; flex: none; background: var(--faint); }
  .dot.ok { background: var(--sage); }
  .dot.bad { background: var(--coral); }
  .runs { margin-top: 6px; display: flex; flex-direction: column; gap: 2px; }
  .log { margin: 2px 0 6px; padding: 10px var(--s3); background: var(--rail); border-radius: 6px; max-height: 260px; overflow: auto; white-space: pre-wrap; word-break: break-all; color: var(--muted); font-size: var(--t-sm); }

  @media (max-width: 760px) {
    .btn { min-height: 44px; }
    .last, .run { min-height: 44px; flex-wrap: wrap; }
  }
</style>
