<script lang="ts">
  import type { Snippet } from 'svelte'
  import { dialog as modal } from '../../ui/dialog.ts'
  let { title, eyebrow = 'Account details', onclose, children }: { title: string; eyebrow?: string; onclose: () => void; children: Snippet } = $props()
  let closeButton = $state<HTMLButtonElement>()
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="veil">
  <div class="drawer" role="dialog" aria-modal="true" aria-labelledby="usage-drawer-title" tabindex="-1" use:modal={{ close: onclose, initial: () => closeButton }}>
    <header><div><div class="eyebrow">{eyebrow}</div><h2 id="usage-drawer-title">{title}</h2></div><button class="close" aria-label="Close account panel" bind:this={closeButton} onclick={onclose}>×</button></header>
    {@render children()}
  </div>
</div>
<style>
  .veil { position: fixed; inset: 0; z-index: 60; background: #0e0c109c; display: flex; justify-content: flex-end; }
  .drawer { width: min(470px, 100%); height: 100%; overflow-y: auto; background: var(--main); border-left: 1px solid var(--line); padding: 28px; box-shadow: -12px 0 40px #0003; outline: none; }
  header { display: flex; align-items: start; justify-content: space-between; gap: var(--s3); margin-bottom: 25px; }
  h2 { font-size: var(--t-page); font-weight: 500; letter-spacing: -.5px; margin: 7px 0 0; overflow-wrap: anywhere; }
  .eyebrow { font-size: var(--t-cap); text-transform: uppercase; letter-spacing: .1em; color: var(--muted); }
  .close { border: 1px solid var(--line); border-radius: 7px; background: var(--raise); font-size: var(--t-display); width: 36px; height: 36px; flex: none; }
  @media (max-width: 540px) { .drawer { padding: 23px 18px max(var(--s5), env(safe-area-inset-bottom)); } h2 { font-size: var(--t-page); } .close { width: 44px; height: 44px; } }
</style>
