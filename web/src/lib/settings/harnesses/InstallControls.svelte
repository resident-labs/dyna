<script lang="ts">
  import { fade, slide } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import { recommendedVersion, type HarnessRow } from './state.ts'

  // Install, update and uninstall controls of the selected harness. The pane
  // runs the actions and owns the typed version and the inline confirm.
  let {
    row,
    busy = false,
    chooseVersion = $bindable(false),
    versionInput = $bindable(''),
    removing = $bindable(''),
    oninstall,
    onuninstall,
  }: {
    row: HarnessRow
    busy?: boolean
    chooseVersion?: boolean
    versionInput?: string
    removing?: string
    oninstall: (pin?: string) => void
    onuninstall: () => void
  } = $props()
</script>

{#if row.installing}
  <div class="installing toolbar" role="status" in:fade={{ duration: motion(BASE) }}>
    <span class="muted small">Installing Dyna’s copy. This can take a minute.</span>
    <span class="progress wide" aria-hidden="true"></span>
  </div>
{:else if row.needsInstall}
  <div class="toolbar install">
    {#if chooseVersion || !recommendedVersion(row)}
      <label class="ver-field" transition:slide={{ duration: motion(BASE) }}>
        <span class="muted small">{recommendedVersion(row) ? 'Version to install' : 'Dyna can’t look up the latest version right now. Enter the version number to install.'}</span>
        <input bind:value={versionInput} aria-label={row.meta.name + ' version'} placeholder={recommendedVersion(row) || 'e.g. 1.0.0'} disabled={busy} />
      </label>
    {/if}
    <span class="btn-group">
      <button class="btn pri" disabled={busy || !row.capability?.installSupported} onclick={() => oninstall()}>Install</button>
      {#if recommendedVersion(row) && !chooseVersion}<button class="link" disabled={busy} onclick={() => (chooseVersion = true)}>Choose version</button>{/if}
    </span>
  </div>
{:else if row.update || row.version}
  <div class="btn-group toolbar">
    {#if removing === 'copy'}
      <InlineConfirm ask="Uninstall Dyna’s copy?" action="Uninstall" {busy} onconfirm={onuninstall} oncancel={() => (removing = '')} />
    {:else}
      {#if row.update}<button class="btn" disabled={busy} onclick={() => oninstall(row.update)}><span class="up" aria-hidden="true">↑</span>Update to {row.update}</button>{/if}
      {#if row.version}<button class="btn quiet" disabled={busy} onclick={() => (removing = 'copy')}>Uninstall</button>{/if}
    {/if}
  </div>
{/if}

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .up { margin-right: 6px; }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .ver-field input { background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 5px 9px; font-size: var(--t-sm); color: var(--ink); min-width: 0; flex: 1; }
  .ver-field { display: grid; gap: var(--s1); }
  .toolbar.install { display: grid; gap: var(--s2); justify-items: start; }
  .toolbar.install .ver-field { justify-self: stretch; }
  .link { font-size: var(--t-sm); color: var(--muted); text-decoration: underline; text-underline-offset: 3px; }
  .link:not(:disabled) { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .link:hover:not(:disabled) { color: var(--ink); } }
  .installing { display: grid; gap: var(--s2); }
  /* An install has no percentage: a short bar sweeps until it ends. The
     global reduced-motion rule stops the sweep. */
  .progress { position: relative; display: block; width: 72px; height: 3px; border-radius: 2px; background: var(--line); overflow: hidden; }
  .progress.wide { width: 100%; }
  .progress::after { content: ''; position: absolute; inset: 0 auto 0 0; width: 40%; border-radius: inherit; background: var(--iris); animation: sweep 1.2s var(--ease) infinite; }
  @keyframes sweep { from { transform: translateX(-100%); } to { transform: translateX(250%); } }
  .toolbar { margin-bottom: var(--s4); }
</style>
