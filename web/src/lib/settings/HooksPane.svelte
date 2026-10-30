<script lang="ts">
  // Hooks: commands that run on repository events, and the built-in automatic
  // passes. The pane serves both screens: Room settings passes the room's
  // repository, and Settings passes scope="global" for the hooks that belong to
  // the machine. Settings holds choices, so the global screen shows no run
  // history or logs: those live on the Home Services card and a repository's
  // Services page. `internal/hooks` does the work, the same code `dyna git hooks`
  // uses. Built-in rows come from the engine's table: the owner changes only
  // their interval, timeout and switch, and `dyna git hooks run <name>
  // --global` runs one by hand.
  import { errorText } from '../errorText.ts'
  import { onMount } from 'svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { api } from '../../api.ts'
  import Skeleton from '../ui/Skeleton.svelte'
  import InlineConfirm from '../ui/InlineConfirm.svelte'
  import Switch from '../ui/Switch.svelte'
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../ui/motion.ts'
  import { ago, duration, until } from '../ui/format.ts'
  import HookForm from './HookForm.svelte'
  import { listedHooks, eventLabel, exitLabel, failLabel, hookForm, hookOf, hookPatch, isBuiltin, authorLabel, newHook, newestFirst, runByHand, runTone, timeoutLabel, type HookFields, type HookInput, type HookScope } from './hooks.ts'
  import type { HookLogTarget } from './hookLog.ts'
  import type { Hook, HookRun, HookRunLog, HookSummary } from '../api/settings.ts'

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

  let { onmodal = null, repo = '', scope = 'repository', focus = null }: { onmodal?: ((open: boolean) => void) | null; repo?: string; scope?: HookScope; focus?: HookLogTarget | null } = $props()

  // global says which screen this is: the machine's hooks, or one repository's.
  // A test runs these functions without props, so the check tolerates no scope.
  const isGlobal = () => typeof scope !== 'undefined' && scope === 'global'

  let hooks = $state<Hook[] | null>(null)
  let runs = $state<Record<string, HookRun[]>>({}) // hook name -> runs, newest first
  let logs = $state<Record<string, HookRunLog>>({}) // run id -> {tail, exitCode}
  let next = $state<Record<string, string>>({}) // hook name -> next scheduled run
  let busy = $state<Record<string, boolean>>({}) // hook name -> true while its row waits
  let open = $state('') // hook name whose runs are showing
  let openRun = $state('') // run id whose log is showing
  let removing = $state<Removing | null>(null) // the hook asked about inline: {name, busy, error}
  let editor = $state<Editor | null>(null) // {hook (null when adding), form, busy, error}
  let error = $state('')
  let note = $state('')
  const shown = 10 // runs listed per hook

  let mine = $derived((hooks ?? []).filter((h) => !isBuiltin(h)))

  // An open form keeps Esc from closing Settings over unsaved input.
  // Unmounting with an editor open (another settings section) frees Esc.
  $effect(() => { onmodal?.(!!editor); return () => onmodal?.(false) })

  function say(text: string) {
    note = text
    setTimeout(() => { if (note === text) note = '' }, 2600)
  }

  async function loadRuns(name: string) {
    const list = newestFirst<HookRun>(await (isGlobal() ? api.globalHookRuns(name) : api.hookRuns(repo, name)))
    runs[name] = list
    if (list[0] && !logs[list[0].id]) await loadLog(name, list[0].id)
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
      const listed: Hook[] = listedHooks((await (isGlobal() ? api.listGlobalHooks() : api.listHooks(repo))) ?? [], isGlobal() ? 'global' : 'repository')
      hooks = listed
      if (!isGlobal()) await Promise.all(listed.map((h) => loadRuns(h.name)))
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
      // A failing pass is a result, not a fault. The new run shows below.
      say(`${h.label || h.name} did not pass.`)
    }
    try {
      if (!isGlobal()) {
        await loadRuns(h.name)
        open = h.name
        for (const r of (runs[h.name] ?? []).slice(0, shown)) if (!logs[r.id]) loadLog(h.name, r.id)
      }
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
      say(`${h.label || h.name} is ${on ? 'on' : 'off'}.`)
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

{#snippet hookRow(h: Hook)}
  {@const list = runs[h.name] ?? []}
  {@const last = list[0]}
  {#if editor?.hook?.name === h.name}
    <HookForm bind:form={editor.form} editing builtin={isBuiltin(h)} busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} />
  {:else}
    <div class="hook" transition:slide={{ duration: motion(BASE) }}>
      <div class="top">
        <div class="head">
          <span class="name">{h.label || h.name}</span>
          {#if isBuiltin(h)}<span class="badge auto">Automatic</span>{/if}
          <span class="badge">{eventLabel(h.event, h.everyMinutes)}</span>
          {#if !isBuiltin(h)}<span class="badge" class:block={h.onFail === 'block'} title={h.onFail === 'block' ? 'A failure stops the land' : 'A failure sends an alert'}>{failLabel(h.onFail)}</span>{/if}
        </div>
        <div class="acts">
          <Switch on={!h.disabled} label={`${h.label || h.name} runs automatically`} disabled={busy[h.name] || !!editor} onchange={(on: boolean) => toggle(h, on)} />
          <button class="btn" disabled={busy[h.name] || !!editor} onclick={() => (editor = { hook: h, form: hookForm(h) })}>Edit</button>
          <button class="btn" disabled={busy[h.name]} onclick={() => run(h)}>{busy[h.name] ? 'Running…' : 'Run now'}</button>
          {#if !isBuiltin(h)}
            {#if removing?.name === h.name}
              <InlineConfirm busy={removing.busy} onconfirm={remove} oncancel={() => (removing = null)} />
            {:else}
              <button class="btn quiet" disabled={busy[h.name]} onclick={() => (removing = { name: h.name })}>Remove</button>
            {/if}
          {/if}
        </div>
      </div>
      {#if removing?.name === h.name && removing.error}<p class="err" role="alert">{removing.error}</p>{/if}
      {#if isBuiltin(h)}
        <div class="byhand"><span class="label">Run by hand</span><code class="mono">{runByHand(h)}</code></div>
      {:else}
        <pre class="cmd mono">{h.command}</pre>
      {/if}
      <div class="meta">
        {#if !isBuiltin(h)}<span>cwd <b class="mono">{h.cwd}</b></span>{/if}
        <span>timeout <b>{timeoutLabel(h.timeout)}</b></span>
        {#if next[h.name]}<span>next run <b>{until(next[h.name])}</b></span>{/if}
        <span>{isBuiltin(h) ? 'built in' : 'added by ' + authorLabel(h.addedBy)}{h.addedBy && !isBuiltin(h) && h.addedAt ? ' · ' + ago(h.addedAt) : ''}</span>
      </div>
      {#if isGlobal()}
        <!-- Settings shows no run history. -->
      {:else if last}
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
{/snippet}

<section>
  {#if error}<p class="err">{error}</p>{/if}
  {#if note}<p class="note" role="status">{note}</p>{/if}

  {#if hooks && !editor}
    <div class="tools"><button class="btn" onclick={() => (editor = { hook: null, form: newHook() })}>+ Add hook</button></div>
  {/if}
  {#if editor && !editor.hook}
    <div class="adding"><HookForm form={editor.form} busy={editor.busy} error={editor.error} onsave={saveHook} oncancel={() => (editor = null)} /></div>
  {/if}

  {#if hooks === null && !error}<Skeleton label="Loading hooks" rows={3} />{/if}

  {#if hooks && !hooks.length && !error && !editor}
    <EmptyState title="No hooks yet">Add one here, or from a terminal in the repo: <code class="mono">dyna git hooks add verify --event before-land --command 'go test ./...' --on-fail block</code></EmptyState>
  {/if}

  {#if hooks && mine.length}
    {#if isGlobal()}<h3 class="section-label">Yours</h3>{/if}
    <div class="list">
      {#each mine as h (h.name)}{@render hookRow(h)}{/each}
    </div>
  {/if}
</section>


<style>
  .err { color: var(--coral); }
  .note { color: var(--sage); font-size: var(--t-ui); margin: 0 0 10px; }
  .tools { display: flex; justify-content: flex-end; margin: 0 0 10px; }
  .adding { margin: 0 0 10px; }
  .quiet { color: var(--muted); font-size: var(--t-ui); margin: 0 0 10px; }
  .list { display: flex; flex-direction: column; gap: 10px; margin: 0 0 var(--s4); }
  .hook { background: var(--side); border: 1px solid var(--line-soft); border-radius: 10px; padding: 14px var(--s4); transition: border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .hook:hover { border-color: var(--line); }
  }
  .top { display: flex; align-items: center; gap: var(--s2) var(--s3); flex-wrap: wrap; }
  .head { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; min-width: 0; }
  /* Each row's controls stay together: the group moves to its own line, and
     the buttons never split across lines. */
  .acts { display: flex; align-items: center; gap: var(--s2); flex-wrap: nowrap; margin-left: auto; flex: none; }
  .name { font-weight: 600; font-size: var(--t-lead); }
  .badge { font-size: var(--t-cap); font-weight: 600; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 1px 6px; }
  .badge.block { color: var(--gold); background: var(--gold-wash); }
  .badge.auto { color: var(--iris); }
  /* A command wraps at its spaces and scrolls when one word is longer than the
     card, so a path or a flag chain is never broken mid-word. */
  .cmd { margin: 10px 0 var(--s2); padding: var(--s2) 10px; background: var(--rail); border-radius: 6px; white-space: pre-wrap; overflow-wrap: normal; word-break: normal; overflow-x: auto; color: var(--ink); }
  .byhand { display: flex; align-items: baseline; gap: var(--s2); margin: 10px 0 var(--s2); flex-wrap: wrap; }
  .byhand .label { color: var(--faint); font-size: var(--t-sm); }
  .byhand code { color: var(--ink); font-size: var(--t-sm); overflow-x: auto; white-space: nowrap; }
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
    /* A phone keeps the controls in one row that scrolls sideways. */
    .acts { margin-left: 0; overflow-x: auto; }
    .last, .run { min-height: 44px; flex-wrap: wrap; }
  }
</style>
