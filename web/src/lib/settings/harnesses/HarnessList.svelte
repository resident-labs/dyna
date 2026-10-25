<script lang="ts">
  import { fade } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import HarnessIcon from './HarnessIcon.svelte'
  import { rowLine, type HarnessRow } from './state.ts'

  // The harness list. The pane owns the selection and every action, so a row
  // only reports what the user picked.
  let {
    rows,
    selected,
    busy = false,
    onselect,
    oninstall,
  }: {
    rows: HarnessRow[]
    selected: string
    busy?: boolean
    onselect: (id: string) => void
    oninstall: (row: HarnessRow, pin?: string) => void
  } = $props()
</script>

<div class="list" aria-label="Harnesses">
  {#each rows as r, i (r.id)}
    <div class="row" class:sel={r.id === selected} style:order={i * 2}>
      <button class="pick" aria-pressed={r.id === selected} onclick={() => onselect(r.id)}>
        <HarnessIcon id={r.meta.iconId} />
        <span class="who">
          <span class="name">{r.meta.name}</span>
          <span class="ver">{rowLine(r)}</span>
        </span>
      </button>
      {#if r.installing}
        <span class="progress" role="progressbar" aria-label={`Installing ${r.meta.name}`} in:fade={{ duration: motion(BASE) }}></span>
      {:else if r.needsInstall}
        <button class="btn pri" disabled={busy || !r.capability?.installSupported} onclick={() => oninstall(r)}>Install</button>
      {:else if r.update}
        <button class="btn" disabled={busy} title={`Update to ${r.update}`} onclick={() => oninstall(r, r.update)}><span class="up" aria-hidden="true">↑</span>Update</button>
      {/if}
    </div>
  {/each}
</div>

<style>
  .up { margin-right: 6px; }
  .ver { font-family: var(--mono); font-size: var(--t-cap); color: var(--muted); }
  .name { font-size: var(--t-body); font-weight: 500; }
  .row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: var(--s3); align-items: center; padding: 11px var(--s3); border-bottom: 1px solid var(--line-soft); border-radius: 8px; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .pick { display: flex; align-items: center; gap: var(--s3); min-width: 0; text-align: left; }
  .who { display: grid; gap: 3px; min-width: 0; }
  .row.sel { background: var(--select); }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); }
  /* An install has no percentage: a short bar sweeps until it ends. The
     global reduced-motion rule stops the sweep. */
  .progress { position: relative; display: block; width: 72px; height: 3px; border-radius: 2px; background: var(--line); overflow: hidden; }
  .progress::after { content: ''; position: absolute; inset: 0 auto 0 0; width: 40%; border-radius: inherit; background: var(--iris); animation: sweep 1.2s var(--ease) infinite; }
  @keyframes sweep { from { transform: translateX(-100%); } to { transform: translateX(250%); } }
</style>
