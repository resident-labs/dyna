<script lang="ts">
  import { slide } from 'svelte/transition'
  import { BASE, motion } from '../../ui/motion.ts'
  import InlineConfirm from '../../ui/InlineConfirm.svelte'
  import type { ProviderKey } from '../../api/harness.ts'
  import { hasSubscriptionLogin, type HarnessRow } from './state.ts'

  // The API-key providers of the selected harness. The pane reads the list and
  // runs the add and remove calls, so this only shows what it is given.
  let {
    row,
    providers = [],
    busy = false,
    removingProvider = $bindable(''),
    onremove,
    onadd,
  }: {
    row: HarnessRow
    providers?: ProviderKey[]
    busy?: boolean
    removingProvider?: string
    onremove: (providerId: string) => void
    onadd: () => void
  } = $props()
</script>

<div class="sec">
  <h3 class="section-label">API-key providers</h3>
  {#if !hasSubscriptionLogin(row.id)}<p class="muted small" style="margin:0 0 var(--s2)">{row.meta.name} has no subscription login. Add a provider with an API key.</p>{/if}
  {#each providers as p (p.id)}
    <div class="acct" transition:slide={{ duration: motion(BASE) }}>
      <span class="acct-name"><span class="dot on" aria-hidden="true"></span><span class="label">{p.name}</span></span>
      <span class="btn-group">
        {#if removingProvider === p.id}
          <InlineConfirm {busy} onconfirm={() => onremove(p.id)} oncancel={() => (removingProvider = '')} />
        {:else}
          <button class="btn quiet" disabled={busy} onclick={() => (removingProvider = p.id)}>Remove</button>
        {/if}
      </span>
    </div>
  {/each}
  <button class="btn quiet add" disabled={busy} onclick={onadd}><span aria-hidden="true">+</span>Add provider</button>
</div>

<style>
  .btn-group { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .muted { color: var(--muted); }
  .small { font-size: var(--t-sm); }
  .sec { border-top: 1px solid var(--line); padding: var(--s4) 0; }
  .sec .section-label { margin-top: 0; }
  .acct { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--s1) var(--s3); min-height: 38px; font-size: var(--t-ui); }
  .acct-name { display: flex; flex: 1 1 15rem; flex-wrap: wrap; align-items: center; gap: var(--s1) var(--s2); min-width: 0; }
  .acct .btn-group { flex-wrap: nowrap; margin-left: auto; justify-content: flex-end; }
  .acct .btn-group .btn { min-width: 4.4rem; padding: 0 10px; }
  .acct-name .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dot { width: 6px; height: 6px; flex: none; border-radius: 50%; border: 1px solid var(--line); }
  .dot.on { background: var(--sage); border-color: var(--sage); }
  .btn.add { margin-top: var(--s1); padding-left: 0; border-color: transparent; color: var(--muted); }
  .btn.add span { margin-right: 6px; }
</style>
