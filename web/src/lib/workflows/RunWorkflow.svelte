<script lang="ts">
  import Segmented from '../ui/Segmented.svelte'
  import { dialog } from '../ui/dialog.ts'
  // Start a workflow run in the room: pick a template (or give a script
  // path on the host) and an input. The input goes as JSON when it parses
  // as JSON, else as a plain string; a chip says which.
  import { onMount, tick } from 'svelte'
  import type { Room } from '../api/room.ts'
  import type { TemplateInfo } from '../api/workflow.ts'
  import { errorText } from '../errorText.ts'
  import { api } from '../../api.js'
  import { roomLabel } from '../ui/format.ts'

  interface Props {
    room: Room
    onstarted: (id?: string) => void
    oncancel: () => void
  }
  let { room, onstarted, oncancel }: Props = $props()

  let templates = $state<TemplateInfo[] | null>(null)
  let error = $state('')
  let filter = $state('')
  let pick = $state('') // a template name, or '' with mode 'script'
  let mode = $state<'template' | 'script'>('template')
  let script = $state('')
  let input = $state('')
  let busy = $state(false)
  let card = $state<HTMLElement>()
  let search = $state<HTMLInputElement>()

  // The source tag helps only when templates come from more than one place.
  let mixed = $derived(new Set((templates ?? []).map((t) => t.source)).size > 1)
  let shown = $derived((templates ?? []).filter((t) => !filter || `${t.name} ${t.description ?? ''}`.toLowerCase().includes(filter.toLowerCase())))
  let parsed = $derived.by((): { kind: string; value: unknown } => {
    const s = input.trim()
    if (!s) return { kind: 'none', value: null }
    try {
      return { kind: 'json', value: JSON.parse(s) }
    } catch {
      return { kind: 'text', value: input }
    }
  })
  let ready = $derived(!busy && (mode === 'script' ? script.trim() !== '' : shown.some((t) => t.name === pick)))

  onMount(async () => {
    try {
      const found: TemplateInfo[] = (await api.listTemplates()) ?? []
      templates = found
      const first = found[0]
      if (first) pick = first.name
      else mode = 'script'
    } catch (e) {
      error = errorText(e)
      templates = []
    }
    await tick()
    if (search?.isConnected) search.focus()
  })

  async function start() {
    if (!ready) return
    busy = true
    error = ''
    try {
      const spec = mode === 'script' ? { script: script.trim() } : { template: pick }
      const res = await api.startRun(room.id, spec, parsed.value)
      onstarted(res?.id)
    } catch (e) {
      error = errorText(e)
      busy = false
    }
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      start()
    }
  }

  // Up and Down move the choice while the search box has focus.
  function searchKey(e: KeyboardEvent) {
    const i = shown.findIndex((t) => t.name === pick)
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const t = shown[Math.max(0, Math.min(shown.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))]
      if (t) {
        pick = t.name
        mode = 'template'
        card?.querySelector(`[data-name="${CSS.escape(t.name)}"]`)?.scrollIntoView({ block: 'nearest' })
      }
    }
  }
  const sourceText: Record<string, string> = { builtin: 'built in', repo: 'this repo', user: 'yours', project: 'this repo' }
</script>


<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" bind:this={card} use:dialog={{ close: oncancel, busy, initial: () => search }} role="dialog" onkeydown={keydown} tabindex="-1" aria-modal="true" aria-label="Run a workflow" onclick={(e) => e.stopPropagation()}>
    <header>
      <h2>Run a workflow</h2>
      <p>It runs in <strong>#{roomLabel(room)}</strong>, from the room's repo folder.</p>
      <button class="x" onclick={oncancel} aria-label="Close">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      </button>
    </header>

    <div class="body">
      <div class="mode"><Segmented label="What to run" options={[['template', 'Template'], ['script', 'Script file']]} value={mode} onchange={(v: 'template' | 'script') => (mode = v)} /></div>

      {#if mode === 'template'}
        <input class="search" bind:this={search} bind:value={filter} onkeydown={searchKey} placeholder="Filter templates" aria-label="Filter templates" autocomplete="off" spellcheck="false" />
        <div class="list" role="radiogroup" aria-label="Templates">
          {#if !templates}
            <p class="quiet">Loading templates…</p>
          {:else if !shown.length}
            <p class="quiet">{templates.length ? 'No template matches.' : 'No templates found. Use a script file instead.'}</p>
          {:else}
            {#each shown as t (t.name)}
              <button class="tpl" role="radio" aria-checked={pick === t.name} class:sel={pick === t.name} data-name={t.name} onclick={() => (pick = t.name)} ondblclick={start}>
                <span class="radio" aria-hidden="true"></span>
                <span class="tt">
                  <span class="name">{t.name}{#if mixed}<span class="src">{sourceText[t.source] ?? t.source}</span>{/if}</span>
                  {#if t.description}<span class="desc">{t.description}</span>{/if}
                </span>
              </button>
            {/each}
          {/if}
        </div>
      {:else}
        <label class="field">
          <span class="lab">Script path on the host</span>
          <input bind:value={script} placeholder="~/workflows/review.js" class="mono" autocomplete="off" autocapitalize="off" spellcheck="false" />
        </label>
      {/if}

      <label class="field">
        <span class="lab">Input <span class="chip {parsed.kind}">{parsed.kind === 'json' ? 'JSON' : parsed.kind === 'text' ? 'Text' : 'None'}</span></span>
        <textarea bind:value={input} rows="4" placeholder={'What the workflow should work on. Plain text, or JSON such as {"files": ["a.go"]}'}></textarea>
      </label>
      {#if error}<p class="err">{error}</p>{/if}
    </div>

    <footer>
      <span class="hint"><kbd>Ctrl</kbd>+<kbd>Enter</kbd> to start</span>
      <button class="ghost" onclick={oncancel} disabled={busy}>Cancel</button>
      <button class="go" onclick={start} disabled={!ready}>{busy ? 'Starting…' : 'Start run'}</button>
    </footer>
  </div>
</div>

<style>
  .mode { align-self: flex-start; }
  .backdrop { position: fixed; inset: 0; background: rgba(8, 8, 10, 0.62); display: grid; place-items: center; z-index: 60; padding: 32px; }
  .card { width: min(560px, 100%); max-height: min(720px, 100%); display: flex; flex-direction: column; background: var(--side); border-radius: 12px; box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5); }
  header { position: relative; padding: 18px 52px 6px 20px; }
  h2 { margin: 0 0 4px; font-size: 18px; font-weight: 600; }
  header p { margin: 0; color: var(--muted); font-size: 13.5px; }
  .x { position: absolute; top: 14px; right: 14px; width: 30px; height: 30px; border-radius: 50%; display: grid; place-items: center; color: var(--muted); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { background: var(--hover); color: var(--ink); }
  }
  .x:active { background: var(--select); }
  .body { padding: 10px 20px 4px; overflow-y: auto; display: flex; flex-direction: column; gap: 12px; min-height: 0; }
  input, textarea { width: 100%; background: var(--rail); border: 1px solid var(--line); border-radius: 7px; color: var(--ink); font: inherit; font-size: 15px; padding: 8px 11px; outline: none; }
  input:focus, textarea:focus { border-color: var(--iris-edge); }
  input.mono { font-family: var(--mono); font-size: 14px; }
  textarea { resize: vertical; font-size: 14px; line-height: 1.45; }
  .list { display: flex; flex-direction: column; gap: 4px; max-height: 260px; overflow-y: auto; margin: -4px -4px 0; padding: 4px; }
  .tpl { flex: none; display: flex; align-items: flex-start; gap: 12px; padding: 10px 12px; border-radius: 8px; text-align: left; border: 1px solid transparent; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tpl:hover { background: var(--hover); }
  }
  .tpl:active { background: var(--select); }
  .tpl.sel { background: var(--select); border-color: var(--iris-edge); }
  .radio { width: 16px; height: 16px; border-radius: 50%; border: 2px solid var(--faint); flex: none; margin-top: 2px; }
  .tpl.sel .radio { border-color: var(--iris); background: radial-gradient(var(--iris) 0 3.5px, transparent 4px); }
  .tt { display: flex; flex-direction: column; min-width: 0; gap: 2px; }
  .name { font-weight: 600; font-size: 14.5px; color: var(--ink); display: flex; align-items: center; gap: 8px; }
  .src { font-size: 11px; font-weight: 600; color: var(--muted); background: var(--raise); border-radius: 4px; padding: 0 6px; }
  .desc { font-size: 13px; color: var(--muted); line-height: 1.4; }
  .field { display: flex; flex-direction: column; gap: 6px; }
  .lab { font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; display: flex; align-items: center; gap: 8px; }
  .chip { text-transform: none; letter-spacing: 0; font-size: 11px; font-weight: 600; border-radius: 4px; padding: 0 6px; background: var(--raise); color: var(--faint); }
  .chip.json { color: var(--iris); background: var(--iris-wash); }
  .chip.text { color: var(--ink-2); }
  .quiet { color: var(--faint); font-size: 13.5px; margin: 6px 4px; }
  .err { color: var(--coral); font-size: 14px; margin: 0; }
  footer { display: flex; align-items: center; gap: 8px; padding: 14px 20px 16px; }
  .hint { flex: 1; font-size: 12px; color: var(--faint); }
  kbd { font-family: var(--sans); font-size: 11px; font-weight: 600; color: var(--muted); }
  footer button { padding: 8px 16px; border-radius: 6px; font-size: 14px; font-weight: 500; }
  .ghost { color: var(--ink-2); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ghost:hover { background: var(--hover); color: var(--ink); }
  }
  .ghost:active { background: var(--select); }
  .go { background: var(--iris); color: #18161d; }
  .go:disabled, .ghost:disabled { opacity: 0.45; cursor: default; }

  @media (max-width: 760px) {
    .backdrop { padding: 0; align-items: end; }
    .card { width: 100%; max-height: 92dvh; border-radius: 14px 14px 0 0; padding-bottom: env(safe-area-inset-bottom); }
    .x { width: 44px; height: 44px; top: 8px; right: 8px; }
    input, textarea { font-size: 16px; }
    .tpl { min-height: 52px; }
    .hint { display: none; }
    footer button { flex: 1; min-height: 44px; }
  }
</style>
