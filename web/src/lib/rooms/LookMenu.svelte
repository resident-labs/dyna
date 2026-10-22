<script lang="ts">
  // The top-bar channel button and the popover it opens.
  // A phone needs the bar for the page title, so the button is replaced by a
  // row in the bar's overflow menu (anchored={false}) and the popover is then
  // fixed inside the viewport instead of hanging off the button.
  import LookRows from './LookRows.svelte'
  import { look } from './look.svelte.js'
  import { differs } from './look.ts'

  let { scope, anchored = true, open = $bindable(false) }: { scope: string; anchored?: boolean; open?: boolean } = $props()
  let own = $derived(differs(look, scope).length > 0)
</script>

<svelte:window onkeydown={(e) => { if (open && e.key === 'Escape') open = false }} />

{#if anchored || open}
<div class="wrap">
  {#if anchored}
  <button class="icon" class:on={open || own} onclick={() => (open = !open)} title="Customize this channel" aria-label="Customize this channel" aria-expanded={open} aria-haspopup="dialog">
    <svg viewBox="0 0 24 24" width="21" height="21" aria-hidden="true"><path d="M4 7h9M17 7h3M4 17h3M11 17h9" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /><circle cx="15" cy="7" r="2.2" fill="none" stroke="currentColor" stroke-width="1.8" /><circle cx="9" cy="17" r="2.2" fill="none" stroke="currentColor" stroke-width="1.8" /></svg>
    {#if own}<span class="dot" aria-hidden="true"></span>{/if}
  </button>
  {/if}
  {#if open}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="catch" onclick={() => (open = false)}></div>
    <div class="pop" role="dialog" aria-label="Customize this channel">
      <div class="head">This channel</div>
      <LookRows {scope} />
    </div>
  {/if}
</div>
{/if}

<style>
  .wrap { position: relative; }
  .icon { position: relative; color: var(--muted); display: grid; place-items: center; width: 32px; height: 32px; border-radius: 6px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .icon:hover { color: var(--ink); background: var(--hover); }
  }
  .icon:active { background: var(--select); }
  .icon.on { color: var(--ink); }
  .dot { position: absolute; top: 5px; right: 5px; width: 7px; height: 7px; border-radius: 50%; background: var(--iris); }
  .catch { position: fixed; inset: 0; z-index: 40; }
  .pop { position: absolute; z-index: 41; top: calc(100% + 6px); right: 0; width: 380px; max-width: calc(100vw - 16px); background: #121113; border: 1px solid var(--line-soft); border-radius: 8px; padding: 6px; box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5); }
  .head { font-size: var(--t-sm); font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; padding: 6px var(--s3) var(--s1); }
  @media (max-width: 760px) {
    .icon { width: 44px; height: 44px; }
    /* The button sits near the right edge, so pin the popover to the viewport
       instead: a fixed popover keeps 8 px on each side at any width. */
    .pop { position: fixed; top: calc(56px + env(safe-area-inset-top)); left: 8px; right: 8px; width: auto; max-width: none; max-height: calc(100dvh - 72px); overflow-y: auto; }
  }
</style>
