<script lang="ts">
  // One surface for grouped content: usage, alerts, a PR lane. The optional
  // header is a quiet title, a count and one trailing action.
  import type { Snippet } from 'svelte'
  let { title = '', count = null, leading, action, children }: { title?: string; count?: number | null; leading?: Snippet; action?: Snippet; children?: Snippet } = $props()
</script>
<section class="card">
  {#if title}<h4>{#if leading}{@render leading()}{/if}{title}{#if count !== null}<span class="n">{count}</span>{/if}{#if action}<span class="act">{@render action?.()}</span>{/if}</h4>{/if}
  {@render children?.()}
</section>
<style>
  .card { display: flex; flex-direction: column; gap: var(--s2); min-width: 0; padding: var(--s3) var(--s4); background: var(--side); border-radius: var(--r-sm); box-shadow: inset 0 0 0 1px var(--hair); }
  h4 { display: flex; align-items: baseline; gap: 6px; margin: 0; font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .n { color: var(--faint); font-weight: 500; }
  .act { margin-left: auto; font-weight: 500; }
</style>
