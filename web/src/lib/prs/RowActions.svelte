<script lang="ts">
  // The compact actions at the end of a PR or issue row: one icon button that
  // adds the row to the room composer, and a "⋯" menu that holds the rest.
  // The row group that holds this component sets --row-actions-visibility, so
  // a desktop row shows the actions when it is hovered or focused and a phone
  // shows them all the time (see the .item rules in IssueList and GitHub).
  // The caller names the row, so every tooltip and label says what it acts on.
  import Menu from '../ui/Menu.svelte'
  import type { RowAction } from './props.ts'

  interface Props {
    name: string
    onmention?: (() => void) | null
    items?: RowAction[]
  }
  let { name, onmention = null, items = [] }: Props = $props()

  let menu = $state<{ x: number; y: number } | null>(null)
  let more = $state<HTMLButtonElement | undefined>()

  function openMore() {
    const rect = more?.getBoundingClientRect()
    if (rect) menu = { x: rect.left, y: rect.bottom + 4 }
  }
  // Close returns the focus to the button that opened the menu.
  function closeMore() {
    menu = null
    more?.focus({ preventScroll: true })
  }
</script>

{#if onmention || items.length}
  <div class="row-actions" class:pinned={!!menu}>
    {#if onmention}
      <button type="button" class="act" onclick={onmention} title="Mention {name} in chat" aria-label="Mention {name} in chat">
        <svg viewBox="0 0 20 20" width="17" height="17" aria-hidden="true"><path d="M4 6.5A2.5 2.5 0 0 1 6.5 4h7A2.5 2.5 0 0 1 16 6.5v5A2.5 2.5 0 0 1 13.5 14H9.4l-3.6 2.6V14h-.3A2.5 2.5 0 0 1 4 11.5Z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round" /><path d="M10 6.6v5.2M7.4 9.2h5.2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" /></svg>
      </button>
    {/if}
    {#if items.length}
      <button type="button" class="act" bind:this={more} onclick={openMore} title="More actions for {name}" aria-label="More actions for {name}" aria-haspopup="menu" aria-expanded={!!menu}>
        <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><circle cx="5" cy="10" r="1.6" fill="currentColor" /><circle cx="10" cy="10" r="1.6" fill="currentColor" /><circle cx="15" cy="10" r="1.6" fill="currentColor" /></svg>
      </button>
    {/if}
  </div>
{/if}

{#if menu}
  <Menu x={menu.x} y={menu.y} title={name} {items} onclose={closeMore} />
{/if}

<style>
  .row-actions { display: flex; align-items: center; flex: none; gap: 2px; visibility: var(--row-actions-visibility, visible); }
  /* The menu lives outside the row, so keep its button while it is open. */
  .row-actions.pinned { visibility: visible; }
  .act { display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border-radius: 6px; color: var(--muted); }
  .act:hover { color: var(--ink); background: var(--raise-hover); }
  .act:active { background: var(--select); }
  @media (hover: none) { .act { width: 36px; height: 36px; } }
</style>
