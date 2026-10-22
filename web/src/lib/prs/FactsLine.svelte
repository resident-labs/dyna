<script lang="ts">
  // The facts line under the title of a pull request or an issue: who wrote
  // it, then the facts that item adds (children), then its labels.
  //
  // Props:
  //   author     the author's name; onauthor opens their session, or null
  //   labels     GitHub labels ({name, color})
  //   children   the parts between author and labels, as <span class="part">
  import type { Snippet } from 'svelte'
  import type { GitHubLabel } from '../api/github.ts'
  import { labelStyle } from './github.ts'

  interface Props {
    author?: string
    onauthor?: (() => void) | null
    labels?: GitHubLabel[] | null
    children?: Snippet | null
  }
  let { author = '', onauthor = null, labels = [], children = null }: Props = $props()
</script>

<p class="factsline">
  {#if author}<span class="part">Author {#if onauthor}<button class="who author" onclick={onauthor}>{author}</button>{:else}<span class="who author">{author}</span>{/if}</span>{/if}
  {@render children?.()}
  {#each labels as l (l.name)}<span class="label" style={labelStyle(l.color)}>{l.name}</span>{/each}
</p>

<style>
  .factsline { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2) var(--s4); margin: var(--s3) 0 0; padding-top: var(--s3); border-top: 1px solid var(--line-soft); font-size: var(--t-sm); color: var(--faint); }
  .factsline :global(.part) { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
  .factsline :global(.who) { color: var(--ink); overflow-wrap: anywhere; }
  .factsline :global(button.who:hover) { text-decoration: underline; }
  .label { font-size: 11px; font-weight: 600; padding: 0 7px; border-radius: 9px; line-height: 17px; }
</style>
