<script>
  import { ago } from '../ui/format.ts'
  import { opRunning, opLine, canExport, popoverPlacement } from './sync.ts'

  // The room header's sync badge. remotes comes from remoteList: origin
  // first. The badge shows origin only; the popover lists every remote.
  // In sync is a quiet badge; any other state takes a color. status is the
  // repo's saved answer ({integration, operation}); note is the last action
  // error. Refresh merges origin in; Export opens the export PR.
  let { remotes = [], status = null, note = '', onrefresh, onexport } = $props()

  let main = $derived(remotes[0])
  let open = $state(false)
  let box = $state()
  // Where the popover sits, from the badge's own box, so a narrow header keeps
  // it on screen.
  let place = $state(null)
  let op = $derived(status?.operation)
  let running = $derived(opRunning(op))
  let commits = $derived(status?.integration?.commits?.length ?? 0)
  const tone = { synced: '', ahead: 'iris', behind: 'gold', diverged: 'gold', error: 'coral' }

  function placeNow() {
    if (!box) return
    const r = box.getBoundingClientRect()
    place = popoverPlacement(r.left, r.right, window.innerWidth)
  }

  function toggle() {
    open = !open
    if (open) placeNow()
  }
</script>

<svelte:window onkeydown={(e) => { if (e.key === 'Escape') open = false }} onpointerdown={(e) => { if (open && box && !box.contains(e.target)) open = false }} onresize={() => { if (open) placeNow() }} />

{#if main}
  <div class="sync" bind:this={box}>
    <button class="badge {tone[main.state]}" class:quiet={main.state === 'synced'} onclick={toggle} aria-expanded={open} aria-label="Sync with {main.name}: {main.label}" title="Sync with {main.name}: {main.label}">
      <span class="name">{main.name}</span>
      <span class="label">{main.label}</span>
    </button>
    {#if open}
      <div class="pop" role="dialog" aria-label="Remotes" style:left={place ? `${place.left}px` : null} style:width={place ? `${place.width}px` : null}>
        {#each remotes as r (r.name)}
          <div class="rem">
            <span class="rn">{r.name}{#if r.canonical} <span class="canon">canonical</span>{/if}</span>
            <span class="st {tone[r.state]}">{r.label}</span>
            {#if r.state === 'diverged'}<p class="note">{r.rewrite ? `The remote rewrote history (${r.rewrite}).` : `${r.name} has ${r.behind} new commits and this repo has ${r.ahead}. Refresh before you export.`}</p>{/if}
            {#if r.lastError}<p class="note err">{r.lastError}</p>{/if}
            <time>checked {ago(r.checkedAt)}</time>
          </div>
        {/each}
        {#if opLine(status?.operation) || note}
          <p class="note" class:err={status?.operation?.state === 'failed' || status?.operation?.state === 'conflict'}>{note || opLine(status?.operation)}{#if status?.operation?.prURL && status.operation.state === 'done'} · <a href={status.operation.prURL} target="_blank" rel="noreferrer">PR</a>{/if}</p>
        {/if}
        <div class="acts">
          <button onclick={onrefresh} disabled={running} class:busy={running && op.kind === 'refresh'} aria-busy={running && op.kind === 'refresh'}>{#if running && op.kind === 'refresh'}<span class="spin" aria-hidden="true"></span>Refreshing… {op.state}{:else}Refresh{/if}</button>
          <button class="go" onclick={onexport} disabled={!canExport(status)} class:busy={running && op.kind === 'export'} aria-busy={running && op.kind === 'export'}>{#if running && op.kind === 'export'}<span class="spin" aria-hidden="true"></span>Exporting… {op.state}{:else}{commits ? `Export ${commits} ${commits === 1 ? 'commit' : 'commits'}` : 'Export'}{/if}</button>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .sync { position: relative; flex: none; }
  .badge { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 9px; border-radius: 12px; font-size: var(--t-sm); color: var(--muted); background: var(--raise); white-space: nowrap; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .name { font-weight: 600; color: var(--ink-2); }
  .quiet { background: transparent; color: var(--faint); }
  .badge.gold { color: var(--gold); background: rgba(230, 192, 123, 0.13); }
  .badge.iris { color: var(--iris); background: var(--iris-wash); }
  .badge.coral { color: var(--coral); background: rgba(239, 138, 128, 0.14); }
  .st.gold { color: var(--gold); }
  .st.iris { color: var(--iris); }
  .st.coral { color: var(--coral); }
  .pop { position: absolute; top: calc(100% + 6px); right: 0; z-index: 30; width: 300px; max-width: calc(100vw - 16px); padding: var(--s2) 0; background: var(--raise); border: 1px solid var(--line); border-radius: var(--r-sm); box-shadow: 0 10px 40px rgba(0, 0, 0, 0.35); }
  .rem { display: grid; grid-template-columns: 1fr auto; gap: 2px 12px; padding: var(--s2) var(--s3); }
  .rem + .rem { border-top: 1px solid var(--line-soft); }
  .rn { font-weight: 600; font-size: var(--t-ui); }
  .canon { font-weight: 600; font-size: var(--t-cap); color: var(--iris); margin-left: var(--s1); }
  .st { font-size: var(--t-sm); color: var(--muted); }
  .note { grid-column: 1 / -1; margin: 0; font-size: var(--t-sm); color: var(--ink-2); line-height: var(--leading); }
  .note.err { color: var(--coral); font-family: var(--mono); font-size: var(--t-cap); overflow-wrap: anywhere; }
  .acts { display: flex; gap: var(--s2); padding: var(--s2) var(--s3) var(--s1); border-top: 1px solid var(--line-soft); }
  .acts button { font-size: var(--t-sm); padding: 3px 9px; border-radius: 6px; background: var(--raise); color: var(--ink-2); box-shadow: inset 0 0 0 1px var(--line); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  .acts button.go { background: var(--iris); color: var(--rail); font-weight: 600; box-shadow: none; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .acts button:hover { background: var(--hover); }
    .acts button.go:hover { background: var(--iris); }
  }
  .acts button:active { background: var(--select); }
  .acts button.go:active { background: var(--iris); }
  .acts button:disabled { opacity: 0.45; cursor: default; }
  .acts button.busy:disabled { opacity: 1; cursor: progress; }
  .spin { display: inline-block; width: 10px; height: 10px; margin-right: 6px; border-radius: 50%; border: 2px solid currentColor; border-right-color: transparent; vertical-align: -1px; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .pop > .note { margin: 0; padding: var(--s2) var(--s3) 0; border-top: 1px solid var(--line-soft); font-size: var(--t-sm); color: var(--ink-2); }
  .pop > .note.err { color: var(--coral); }
  time { grid-column: 1 / -1; font-size: var(--t-cap); color: var(--faint); }
</style>
