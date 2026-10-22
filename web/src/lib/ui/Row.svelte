<script lang="ts">
  // One list row for every list in the app: a lead (glyph, icon or avatar),
  // the text, a meta line (`clamp` cuts the text at two lines), an optional
  // detail under it, an optional trail and hover actions. `dense` is the
  // one-line form for cards. With `onclick` the row is a button; rows with
  // actions stay a div, so the action buttons do not nest.
  // `selected` makes the row a toggle; leave it out for a plain action row.
  // `children` replaces the text and meta, for example with an edit box.
  import type { Snippet } from 'svelte'
  interface Props {
    text?: string
    clamp?: boolean
    dense?: boolean
    selected?: boolean
    context?: boolean
    onclick?: ((event: MouseEvent) => void) | null
    lead?: Snippet
    meta?: Snippet
    detail?: Snippet
    trail?: Snippet
    actions?: Snippet
    children?: Snippet
  }
  let { text = '', clamp = true, dense = false, selected = undefined, context = false, onclick = null, lead, meta, detail, trail, actions, children }: Props = $props()
</script>
{#snippet inner()}
  {#if lead}<span class="lead">{@render lead()}</span>{/if}
  <span class="body">{#if children}{@render children()}{:else}<span class="text" class:clamp>{text}</span>{#if meta}<span class="meta">{@render meta()}</span>{/if}{@render detail?.()}{/if}</span>
  {@render trail?.()}
  {#if actions}<span class="actions">{@render actions()}</span>{/if}
{/snippet}
{#if onclick}<button class="row" class:dense class:sel={selected} class:context aria-pressed={selected} {onclick}>{@render inner()}</button>{:else}<div class="row" class:dense class:sel={selected} class:context>{@render inner()}</div>{/if}
<style>
  .row { display: flex; align-items: start; gap: var(--s3); padding: var(--s3); width: 100%; border-radius: var(--r-xs); text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); } .row.sel { background: var(--select); } .row.context { opacity: .6; }
  .lead { padding-top: 3px; flex: none; }
  .body { flex: 1; min-width: 0; }
  .text { font-size: var(--t-body); color: var(--ink); line-height: var(--leading); display: block; overflow-wrap: anywhere; }
  .text.clamp { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .meta { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 10px; color: var(--muted); font-size: var(--t-sm); margin-top: 6px; line-height: var(--leading-tight); }
  .actions { display: flex; gap: var(--s1); opacity: 0; }
  .row:focus-within .actions { opacity: 1; }
  .row:hover .actions { opacity: 1; }
  @media (hover: none) { .actions { opacity: 1; } }
  /* Dense rows sit in a card: one line, centered, a hairline between rows. */
  @media (hover: hover) { .row.dense:hover { background: var(--raise-hover); } }
  .row.dense:active, .row.dense.sel { background: var(--select); }
  .dense { align-items: center; min-height: 40px; padding: var(--s2); }
  .dense .lead { padding-top: 0; }
  .dense .text.clamp { -webkit-line-clamp: 1; }
  .dense .meta { margin-top: 2px; }
  .dense + .dense { box-shadow: 0 -1px 0 var(--hair); }
  @media (max-width: 760px) { .row { gap: 9px; } }
</style>
