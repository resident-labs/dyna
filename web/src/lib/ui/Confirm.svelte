<script lang="ts">
  import { dialog } from './dialog.js'
  // A confirm for an action that is hard to undo. Focus starts on Cancel, so
  // a stray Enter or Space never runs the action.
  interface Props {
    title: string
    lines?: string[]
    action?: string
    danger?: boolean
    busy?: boolean
    error?: string
    onconfirm: () => void
    oncancel: () => void
    alternative?: string
    onalternative?: () => void
  }

  let { title, lines = [], action = 'Confirm', danger = true, busy = false, error = '', onconfirm, oncancel, alternative = '', onalternative }: Props = $props()

  let cancel: HTMLButtonElement | undefined = $state()

</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div class="backdrop">
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="card" use:dialog={{ close: oncancel, busy, initial: () => cancel }} role="alertdialog" tabindex="-1" aria-modal="true" aria-label={title} onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    {#each lines as l}<p>{l}</p>{/each}
    {#if error}<p class="err">{error}</p>{/if}
    <div class="row">
      <button class="ghost" bind:this={cancel} onclick={oncancel} disabled={busy}>Cancel</button>
      {#if alternative}<button class="go" onclick={onalternative} disabled={busy}>{alternative}</button>{/if}
      <button class="go" class:danger onclick={onconfirm} disabled={busy}>{busy ? 'Working…' : action}</button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(8, 8, 10, 0.62);
    display: grid;
    place-items: center;
    z-index: 60;
    padding: var(--s6);
  }
  .card {
    width: min(440px, 100%);
    background: var(--side);
    border-radius: 12px;
    box-shadow: 0 24px 64px rgba(0, 0, 0, 0.5);
    padding: 20px 20px var(--s4);
  }
  h2 { margin: 0 0 10px; font-size: var(--t-head); font-weight: 600; }
  p { margin: 0 0 var(--s2); color: var(--ink-2); font-size: var(--t-body); line-height: var(--leading); }
  .err { color: var(--coral); }
  .row { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: var(--s2); margin-top: var(--s4); }
  .row button { padding: var(--s2) var(--s4); border-radius: 6px; font-size: var(--t-body); font-weight: 500; }
  .ghost { color: var(--ink-2); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .ghost:hover { background: var(--hover); color: var(--ink); }
  }
  .ghost:active {
    background: var(--select);
}
  .go { background: var(--iris); color: #18161d; }
  .go.danger { background: #c9564b; color: #fff; }
  .go:disabled, .ghost:disabled { opacity: 0.6; cursor: default; }
</style>
