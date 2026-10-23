<script lang="ts">
  // The room's issues, one row each in the PRs list style (issueGroups in
  // github.ts orders them). A row has the state icon, the title, the number,
  // author, age, one state chip, labels and the linked PR. A row that needs an owner
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
  import { ago } from '../ui/format.ts'
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
    {#each g.rows as it (it.number)}
      {@const chip = issueChip(it)}
      {@const open = it.linkedPRs?.find((p) => p.state === 'OPEN') ?? it.linkedPRs?.[0]}
      <div class="item">
        <Row selected={it.number === sel} context={g.id === 'closed'} onclick={() => onpick(it)}>
          {#snippet lead()}<PRGlyph kind="issue" {it} />{/snippet}
          <div class="title">{it.title}</div>
          <div class="identity">#{it.number}{#if it.author?.login} <span>by {it.author.login}</span>{/if}<span>{ago(it.updatedAt)}</span>{#if it.commentCount}<span>{it.commentCount} {it.commentCount === 1 ? 'comment' : 'comments'}</span>{/if}</div>
          <div class="facts">
            {#if chip}<Chip tone={chip.tone} outline={!chip.tone}>{chip.label}</Chip>{/if}
            {#each (it.labels ?? []).slice(0, 3) as l}<span class="label" style={labelStyle(l.color)}>{l.name}</span>{/each}
            {#if open && g.id !== 'has-pr'}<span class="pr">PR #{open.number}{open.isDraft ? ' draft' : ''}</span>{/if}
          </div>
        </Row>
        {#if g.id === 'needs-owner'}<button class="start" disabled={starting === it.number} onclick={() => onstart(it)} title="Hand this issue to an agent">Start</button>{/if}
      </div>
    {/each}
  {/each}
</div>

<style>
  .rows { flex: 1; overflow-y: auto; padding: 0 var(--s5) var(--s4); }
  .item { display: flex; align-items: center; gap: var(--s2); border-bottom: 1px solid var(--line-soft); }
  .item :global(.row) { flex: 1; min-width: 0; border-radius: 0; padding: var(--s4) 0; }
  .title { font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .identity { margin-top: var(--s1); color: var(--muted); font-size: var(--t-sm); display: flex; gap: var(--s2); flex-wrap: wrap; }
  .facts { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); margin-top: var(--s2); }
  .facts:empty { display: none; }
  .pr { margin-left: auto; color: var(--muted); font-size: var(--t-sm); }
  .label { font-size: 11px; font-weight: 600; padding: 0 7px; border-radius: 9px; line-height: 17px; }
  .start { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 12px; }
  .start:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .start:disabled { opacity: 0.5; }
  @media (max-width: 760px) { .rows { padding-left: var(--s4); padding-right: var(--s4); } .start { min-height: 44px; padding: 0 14px; } .pr { margin-left: 0; } }
</style>
