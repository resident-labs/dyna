<script lang="ts">
  import { onMount, tick } from 'svelte'
  import type { Room } from '../api/room.ts'
  import type { WorkflowInfo } from '../api/workflow.ts'
  import { errorText } from '../errorText.ts'
  import { api } from '../../api.ts'
  import { roomLabel } from '../ui/format.ts'
  import { dialog } from '../ui/dialog.ts'

  interface Props {
    room: Room
    onstarted: (id?: string) => void
    oncancel: () => void
  }
  let { room, onstarted, oncancel }: Props = $props()
  let workflows = $state<WorkflowInfo[] | null>(null)
  let error = $state('')
  let filter = $state('')
  let pick = $state('')
  let input = $state('')
  let busy = $state(false)
  let card = $state<HTMLElement>()
  let search = $state<HTMLInputElement>()
  // The resolver uses project, then personal, then bundled for duplicate names.
  let available = $derived((workflows ?? []).filter((w, i, all) => all.findIndex((other) => other.name === w.name) === i))
  let shown = $derived(available.filter((w) => `${w.name} ${w.description}`.toLowerCase().includes(filter.toLowerCase())))
  let selected = $derived(available.find((w) => w.name === pick))
  const hints: Record<string, string> = {
    audit: '{"what": "", "paths": [""]}',
    'deep-research': '{"question": ""}',
    'fix-until': '{"command": "", "maxRounds": 3}',
    migrate: '{"instruction": "", "glob": ""}',
  }
  let hint = $derived(selected?.scope === 'bundled' ? hints[selected.name] : undefined)
  let ready = $derived(!busy && shown.some((w) => w.name === pick))

  onMount(async () => {
    try {
      workflows = (await api.listWorkflows(room.id)) ?? []
      pick = workflows[0]?.name ?? ''
    } catch (e) {
      error = errorText(e)
      workflows = []
    }
    await tick()
    if (search?.isConnected) search.focus()
  })
  async function start() {
    if (!ready) return
    let value: unknown = null
    if (input.trim()) {
      try { value = JSON.parse(input) } catch {
        error = 'Enter valid JSON for the workflow arguments.'
        return
      }
    }
    busy = true
    error = ''
    try {
      const res = await api.startNamedRun(room.id, pick, value)
      onstarted(res.id)
    } catch (e) {
      error = errorText(e)
      busy = false
    }
  }
  function keydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); start() }
  }
  function searchKey(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const i = shown.findIndex((w) => w.name === pick)
    const w = shown[Math.max(0, Math.min(shown.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))]
    if (w) {
      pick = w.name
      card?.querySelector(`[data-name="${CSS.escape(w.name)}"]`)?.scrollIntoView({ block: 'nearest' })
    }
  }
</script>

<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" bind:this={card} use:dialog={{ close: oncancel, busy, initial: () => search }} role="dialog" onkeydown={keydown} tabindex="-1" aria-modal="true" aria-label="Run a workflow" onclick={(e) => e.stopPropagation()}>
    <header>
      <h2>Run a workflow</h2>
      <p>It runs in <strong>#{roomLabel(room)}</strong>, from the room's repo folder.</p>
      <button class="x" onclick={oncancel} aria-label="Close"><svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg></button>
    </header>
    <div class="body">
      <input class="search" bind:this={search} bind:value={filter} onkeydown={searchKey} placeholder="Filter workflows" aria-label="Filter workflows" autocomplete="off" spellcheck="false" />
      <div class="list" role="radiogroup" aria-label="Workflows">
        {#if !workflows}<p class="quiet">Loading workflows…</p>
        {:else if !shown.length}<p class="quiet">{workflows.length ? 'No workflow matches.' : 'No workflows found.'}</p>
        {:else}
          {#each shown as w (w.name)}
            <button class="tpl" role="radio" aria-checked={pick === w.name} class:sel={pick === w.name} data-name={w.name} onclick={() => (pick = w.name)}>
              <span class="radio" aria-hidden="true"></span><span class="tt"><span class="name">{w.name}<span class="src">{w.scope}</span></span>{#if w.description}<span class="desc">{w.description}</span>{/if}</span>
            </button>
          {/each}
        {/if}
      </div>
      {#if selected}
        <label class="field"><span class="lab">Arguments (JSON)</span><textarea bind:value={input} rows="4" placeholder={hint ?? 'Optional JSON arguments'}></textarea></label>
      {/if}
      {#if error}<p class="err" role="alert">{error}</p>{/if}
    </div>
    <footer><span class="hint"><kbd>Ctrl</kbd>+<kbd>Enter</kbd> to run</span><button class="ghost" onclick={oncancel} disabled={busy}>Cancel</button><button class="go" onclick={start} disabled={!ready}>{busy ? 'Starting…' : 'Run'}</button></footer>
  </div>
</div>

<style>
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
