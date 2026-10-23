<script lang="ts">
  // "Remove? [Remove] [Cancel]" in place of the button that was pressed, for
  // removing one row. Use Confirm when the choice needs more than a line.
  // While armed it owns Escape (data-esc tells the dialog): the first Escape
  // cancels it, the next closes the dialog. Cancel takes focus so Escape
  // reaches it.
  import { slide } from 'svelte/transition'
  import { BASE, motion } from './motion.ts'
  interface Props {
    ask?: string
    action?: string
    busy?: boolean
    onconfirm: () => void
    oncancel: () => void
  }
  let cancel: HTMLButtonElement | undefined
  $effect(() => cancel?.focus({ preventScroll: true }))
  let { ask = 'Remove?', action = 'Remove', busy = false, onconfirm, oncancel }: Props = $props()
</script>

<span class="inline-confirm" data-esc role="group" aria-label={ask} onkeydown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); oncancel() } }} transition:slide={{ axis: 'x', duration: motion(BASE) }}>
  <span class="ask">{ask}</span>
  <button class="btn danger" disabled={busy} onclick={onconfirm}>{action}</button>
  <button bind:this={cancel} class="btn quiet" disabled={busy} onclick={oncancel}>Cancel</button>
</span>

<style>
  .inline-confirm { display: inline-flex; align-items: center; gap: var(--s2); white-space: nowrap; }
  @media (max-width: 760px) {
    .inline-confirm .btn { min-height: 44px; }
  }
  .ask { font-size: var(--t-sm); color: var(--ink-2); }
</style>
