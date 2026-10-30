<script lang="ts">
  // One surface for grouped content: usage, alerts, a PR lane. The optional
  // header is a quiet title, a count and one trailing action. flush drops the
  // body padding, so rows and tiles reach the edge; divided draws a soft line
  // between the body's children; footer is a quiet line under a top border.
  // A Card inside a Card goes flat: no surface and no border of its own.
  import type { Snippet } from 'svelte'
  interface Props {
    title?: string
    count?: number | null
    leading?: Snippet
    action?: Snippet
    footer?: Snippet
    flush?: boolean
    divided?: boolean
    children?: Snippet
  }
  let { title = '', count = null, leading, action, footer, flush = false, divided = false, children }: Props = $props()
</script>
<section class="card" class:flush data-ui-card>
  {#if title}<h4>{#if leading}{@render leading()}{/if}{title}{#if count !== null}<span class="n">{count}</span>{/if}{#if action}<span class="act">{@render action()}</span>{/if}</h4>{/if}
  {#if divided}<div class="divided">{@render children?.()}</div>{:else}{@render children?.()}{/if}
  {#if footer}<footer>{@render footer()}</footer>{/if}
</section>
<style>
  .card { display: flex; flex-direction: column; gap: var(--s2); min-width: 0; padding: var(--s3) var(--s4); background: var(--surface); border: 1px solid var(--border); border-radius: var(--r-card); }
  h4 { display: flex; align-items: baseline; gap: 6px; margin: 0; font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .n { color: var(--faint); font-weight: 500; }
  .act { margin-left: auto; font-weight: 500; }
  .divided { display: flex; flex-direction: column; min-width: 0; }
  .divided > :global(* + *) { border-top: 1px solid var(--border-soft); }
  footer { margin: 0 calc(-1 * var(--s4)) calc(-1 * var(--s3)); padding: var(--s2) var(--s4); border-top: 1px solid var(--border-soft); font-size: var(--t-sm); color: var(--muted); }
  .flush { gap: 0; padding: 0; }
  .flush > h4 { padding: var(--s3) var(--s4); }
  .flush > footer { margin: 0; }
  .card :global([data-ui-card]) { background: transparent; border-color: transparent; }
</style>
