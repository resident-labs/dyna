<script lang="ts">
  import type { Snippet } from 'svelte'
  // The header of a room work tab such as Goals or Rules: title, blurb,
  // filter chips and an optional usage line under the chips. flush drops the
  // page padding, for a header inside a pane that already has its own.
  interface Props {
    title: string
    blurb?: string
    filters?: { id: string; label: string }[]
    filter?: string
    usage?: string
    action?: Snippet
    flush?: boolean
  }
  let { title, blurb = '', filters = [], filter = $bindable(), usage = '', action, flush = false }: Props = $props()
</script>
<header class:flush><div><h1>{title}</h1><p>{blurb}</p></div><div class="side">{@render action?.()}<div class="filters">{#each filters as f}<button class:sel={filter === f.id} aria-pressed={filter === f.id} onclick={() => (filter = f.id)}>{f.label}</button>{/each}</div>{#if usage}<p class="usage">{usage}</p>{/if}</div></header>
<style>
  header { display: flex; align-items: start; justify-content: space-between; gap: 14px; padding: var(--s5) var(--s5) 18px; }
  header p:empty { display: none; }
  header.flush { padding: 0 0 var(--s4); }
  h1 { margin: 0; font-size: var(--t-page); font-weight: 600; }
  header p { font-size: var(--t-ui); color: var(--muted); margin: 5px 0 0; max-width: 640px; }
  .side { display: flex; flex-direction: column; align-items: flex-end; }
  .filters { display: flex; gap: var(--s1); padding-top: var(--s1); }
  .filters button { border-radius: 14px; padding: var(--s1) 10px; font-size: var(--t-sm); color: var(--muted); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .filters button:hover { background: var(--hover); }
  }
  .filters button:active { background: var(--select); }
  .filters .sel { background: var(--select); color: var(--ink); }
  header .usage { font-size: var(--t-sm); color: var(--faint); text-align: right; margin-top: 6px; font-variant-numeric: tabular-nums; }
  @media (max-width: 760px) { header { padding: 20px var(--s4) 14px; flex-wrap: wrap; } header.flush { padding: 0 0 14px; } }
</style>
