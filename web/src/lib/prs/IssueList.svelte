<script lang="ts">
  // The room's issues, grouped by who is on them (issueGroups in github.js).
  // Each row is the shared Row: the state icon, the title, the number,
  // labels, comments and age, and one state chip. A row that needs an owner
  // has a Start button instead, which hands the issue to an agent.
  //
  // Props:
  //   groups    the issueGroups answer
  //   sel       the open issue's number
  //   starting  the number Start is working on, or 0
  //   onpick    opens an issue; onstart starts work on one
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import PRGlyph from './PRGlyph.svelte'
  import { ago } from '../ui/format.js'
  import { labelStyle, issueChip } from './github.ts'
  import type { GitHubItem } from '../api/github.ts'

  interface Props {
    groups?: { id: string; label: string; rows: GitHubItem[] }[]
    sel?: number
    starting?: number
    onpick: (item: GitHubItem) => void
    onstart: (item: GitHubItem) => void
  }
  let { groups = [], sel = 0, starting = 0, onpick, onstart }: Props = $props()
</script>

<div class="rows">
  {#each groups as g (g.id)}
    <h4 class="grp">{g.label} <span>{g.rows.length}</span></h4>
    {#each g.rows as it (it.number)}
      {@const chip = issueChip(it)}
      <div class="item">
        <Row text={it.title} selected={it.number === sel} context={g.id === 'closed'} onclick={() => onpick(it)}>
          {#snippet lead()}<PRGlyph kind="issue" {it} />{/snippet}
          {#snippet meta()}
            <span>#{it.number}</span>
            {#each (it.labels ?? []).slice(0, 3) as l}<span class="label" style={labelStyle(l.color)}>{l.name}</span>{/each}
            {#if it.commentCount !== undefined}<span>{it.commentCount} {it.commentCount === 1 ? 'comment' : 'comments'}</span>{/if}
            <span>{ago(it.updatedAt)}</span>
          {/snippet}
          {#snippet trail()}{#if chip}<Chip tone={chip.tone} outline={!chip.tone}>{chip.label}</Chip>{/if}{/snippet}
        </Row>
        {#if g.id === 'needs-owner'}<button class="start" disabled={starting === it.number} onclick={() => onstart(it)} title="Hand this issue to an agent">Start</button>{/if}
      </div>
    {/each}
  {/each}
</div>

<style>
  .rows { flex: 1; overflow-y: auto; padding: var(--s2) var(--s2) var(--s4); }
  .grp { margin: var(--s3) var(--s2) var(--s1); font-size: var(--t-sm); font-weight: 600; color: var(--muted); }
  .grp span { color: var(--faint); font-weight: 400; margin-left: 4px; }
  .item { display: flex; align-items: center; gap: var(--s2); }
  .item :global(.row) { flex: 1; min-width: 0; }
  .label { font-size: 11px; font-weight: 600; padding: 0 7px; border-radius: 9px; line-height: 17px; }
  .start { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 12px; margin-right: var(--s2); }
  .start:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .start:disabled { opacity: 0.5; }
  @media (max-width: 760px) { .start { min-height: 44px; padding: 0 14px; } }
</style>
