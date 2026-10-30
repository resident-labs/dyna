<script lang="ts">
  // One page section: a title outside the card, an optional description and
  // one trailing action. The body is usually one flush, divided Card.
  // Sections on a page sit 32 px apart.
  import type { Snippet } from 'svelte'
  interface Props {
    title: string
    description?: string
    action?: Snippet
    id?: string
    children: Snippet
  }
  let { title, description = '', action, id, children }: Props = $props()
</script>

<section class="section" {id}>
  <header>
    <div class="text">
      <h2>{title}</h2>
      {#if description}<p>{description}</p>{/if}
    </div>
    {#if action}<div class="act">{@render action()}</div>{/if}
  </header>
  {@render children()}
</section>

<style>
  .section { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .section:not(:first-child) { margin-top: var(--s6); }
  header { display: flex; align-items: center; gap: var(--s3); min-height: 28px; }
  .text { flex: 1; min-width: 0; }
  h2 { margin: 0; font-size: var(--t-title); font-weight: var(--w-strong); line-height: var(--leading-tight); color: var(--ink); }
  p { margin: 2px 0 0; font-size: var(--t-ui); color: var(--muted); }
  .act { flex: none; display: flex; align-items: center; gap: var(--s2); }
</style>
