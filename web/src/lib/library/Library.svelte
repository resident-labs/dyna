<script lang="ts">
  // The read-only feature map: a tree of entries, the selected entry's
  // detail and the lint findings. Links open the file viewer.
  import TabHeader from '../rooms/TabHeader.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { api } from '../../api.ts'
  import { tick } from 'svelte'
  import { errorText } from '../errorText.ts'
  import type { LibraryView } from '../api/library.ts'
  import { libraryTree, linkTarget, isStale, type LinkTarget } from './library.ts'
  interface Props {
    roomId: string
    narrow?: boolean
    onopen?: (target: LinkTarget) => void
  }
  let { roomId, narrow = false, onopen = () => {} }: Props = $props()
  let view = $state<LibraryView | null>(null)
  let error = $state('')
  let selected = $state('')
  let lintOpen = $state(false)
  // On a phone the tree and the entry are two views: picking an entry opens
  // its detail, and Back returns to the tree where the reader left it.
  let detailOpen = $state(false)
  let tree = $state<HTMLElement>()
  let treeTop = 0
  function pick(id: string) {
    if (narrow && tree) treeTop = tree.scrollTop
    selected = id
    if (narrow) detailOpen = true
  }
  async function back() {
    detailOpen = false
    await tick()
    if (tree) tree.scrollTop = treeTop
  }
  $effect(() => {
    const id = roomId
    api.library(id).then((v: LibraryView) => { if (id === roomId) { view = v; error = '' } }, (e: unknown) => { if (id === roomId) error = errorText(e) })
  })
  let rows = $derived(libraryTree(view?.entries ?? []))
  let byId = $derived(new Map((view?.entries ?? []).map((e) => [e.id, e])))
  let entry = $derived(byId.get(selected) ?? rows[0]?.entry)
  const open = (link: string) => onopen(linkTarget(link))
</script>
{#snippet links(title: string, list: string[] | undefined)}
  {#if list?.length}<div class="field"><h3>{title}</h3><ul>{#each list as l}<li><button class="link mono" onclick={() => open(l)}>{l}</button></li>{/each}</ul></div>{/if}
{/snippet}
<section class="library" aria-label="Library">
  <TabHeader title="Library" blurb="The feature map of this repository, kept by the librarian." />
  {#if error}
    <EmptyState title="Library unavailable">{error}</EmptyState>
  {:else if !view}
    <div class="loading" aria-busy="true"></div>
  {:else if !view.enabled}
    <EmptyState title="The librarian is off">Run <code>dyna library enable</code> in this repository.</EmptyState>
  {:else}
    {#if view.lint.length}
      <details class="lint" bind:open={lintOpen}>
        <summary>{view.lint.length} lint {view.lint.length === 1 ? 'finding' : 'findings'}</summary>
        <ul>{#each view.lint as f}
          <li>{#if f.entry}{@const target = f.entry}<button class="finding" onclick={() => pick(target)}><span class="rule">rule {f.rule}</span> <b>{f.entry}</b> {#if f.file}<span class="mono">{f.file}</span>{/if} {f.message}</button>
            {:else}<span class="finding"><span class="rule">rule {f.rule}</span> {#if f.file}<span class="mono">{f.file}</span>{/if} {f.message}</span>{/if}</li>
        {/each}</ul>
      </details>
    {/if}
    {#if !rows.length}
      <EmptyState title="The map is empty">The librarian adds entries after the next merge.</EmptyState>
    {:else}
      <div class="split" class:narrow>
        {#if !narrow || !detailOpen}
        <nav class="tree" aria-label="Entries" bind:this={tree}>
          {#each rows as { entry: e, depth } (e.id)}
            <button class="node" class:sel={entry?.id === e.id} style="padding-left: {8 + depth * 16}px" aria-pressed={entry?.id === e.id} onclick={() => pick(e.id)}>{e.name}{#if isStale(view, e.id)} <span class="stale" title="Owned files changed since verified">stale</span>{/if}</button>
          {/each}
        </nav>
        {/if}
        {#if entry && (!narrow || detailOpen)}
          {@const parent = entry.parent ? byId.get(entry.parent) : undefined}
          <article class="detail">
            {#if narrow}<button class="back" onclick={back} aria-label="Back to the entries"><span aria-hidden="true">‹</span> Entries</button>{/if}
            <h2>{entry.name}{#if isStale(view, entry.id)} <span class="stale">stale</span>{/if}</h2>
            <p class="meta"><span class="mono">{entry.id}</span> · {entry.kind}{#if parent}&nbsp;· <button class="link" onclick={() => (selected = parent.id)}>{parent.name}</button>{/if}{#if entry.verifiedAt}&nbsp;· verified <span class="mono">{entry.verifiedAt.slice(0, 7)}</span>{/if}</p>
            <p>{entry.summary}</p>
            {#if entry.differs}<p class="differs"><b>Differs:</b> {entry.differs}</p>{/if}
            {@render links('Owns', entry.owns)}
            {@render links('Docs', entry.docs)}
            {@render links('Tests', entry.tests)}
            {#if entry.relates?.length}<div class="field"><h3>Relates</h3><ul>{#each entry.relates as id}{@const related = byId.get(id)}<li>{#if related}<button class="link" onclick={() => (selected = id)}>{related.name}</button>{:else}<span class="mono">{id}</span>{/if}</li>{/each}</ul></div>{/if}
          </article>
        {/if}
      </div>
    {/if}
  {/if}
</section>
<style>
  .library { display: flex; flex-direction: column; min-height: 0; flex: 1; overflow: hidden; }
  .mono { font-family: var(--mono, ui-monospace, monospace); font-size: var(--t-sm); }
  .lint { margin: 0 var(--s5) var(--s3); max-height: 30vh; overflow-y: auto; font-size: var(--t-sm); color: var(--muted); }
  .lint summary { cursor: pointer; }
  .lint ul { list-style: none; margin: var(--s2) 0 0; padding: 0; }
  .finding { text-align: left; padding: 2px 0; color: var(--ink); overflow-wrap: anywhere; }
  .rule { color: var(--faint); }
  .split { display: flex; gap: var(--s5); padding: 0 var(--s5) var(--s5); min-height: 0; flex: 1; }
  .split.narrow { flex-direction: column; gap: var(--s4); padding: 0 var(--s4) var(--s4); }
  .tree { display: flex; flex-direction: column; width: 260px; flex: none; overflow-y: auto; min-height: 0; }
  .narrow .tree { width: auto; flex: 1; }
  .node { text-align: left; padding: var(--s2) 8px; border-radius: var(--r-xs); font-size: var(--t-ui); color: var(--ink); transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .node:hover { background: var(--hover); } }
  .node:active { background: var(--select); } .node.sel { background: var(--select); }
  .stale { margin-left: 6px; font-size: var(--t-cap); color: var(--gold, #d9a441); border: 1px solid currentColor; border-radius: 8px; padding: 0 6px; }
  .detail { flex: 1; min-width: 0; min-height: 0; overflow-y: auto; font-size: var(--t-body); line-height: var(--leading); }
  .detail h2 { margin: 0; font-size: var(--t-head); font-weight: 600; }
  .meta { color: var(--muted); font-size: var(--t-sm); margin: var(--s1) 0 var(--s3); }
  .back { display: inline-flex; gap: var(--s1); align-items: center; padding: var(--s1) var(--s2) var(--s1) 0; margin-bottom: var(--s2); color: var(--muted); font-size: var(--t-ui); transition: color var(--motion-fast) var(--ease); }
  .differs { color: var(--muted); }
  .field h3 { margin: var(--s3) 0 var(--s1); font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .field ul { list-style: none; margin: 0; padding: 0; }
  .link { color: var(--accent, var(--ink)); text-decoration: underline; text-align: left; overflow-wrap: anywhere; }
</style>
