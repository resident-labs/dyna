<script lang="ts">
  // The room's local and GitHub issues, one row each in the PR list style. A row has the state icon, the title, the number,
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
  import { issueReference, type IssueReference, type IssueRow as GitHubItem } from './unifiedIssues.ts'
  import { location } from '../rooms/issues.ts'

  interface Props {
    groups?: { id: string; label: string; rows: GitHubItem[] }[]
    sel?: number
    starting?: number
    onpick: (item: GitHubItem) => void
    onmention?: (reference: IssueReference) => void
    onstart: (item: GitHubItem) => void
    oncloseissue?: (item: GitHubItem) => void
    onexport?: (item: GitHubItem) => void
    ondismiss?: (item: GitHubItem) => void
  }
  let { groups = [], sel = 0, starting = 0, onpick, onstart, onmention, oncloseissue, onexport, ondismiss }: Props = $props()
</script>

<div class="rows">
  {#each groups as g (g.id)}
    {#each g.rows as it (it.number || it.local?.id)}
      {@const unresolvedExport = it.state === 'UNKNOWN' && !!it.local?.githubUrl}
      {@const chip = unresolvedExport ? { tone: '', label: 'Promoted' } : issueChip(it)}
      {@const open = it.linkedPRs?.find((p) => p.state === 'OPEN') ?? it.linkedPRs?.[0]}
      <div class="item">
        <Row selected={!!it.number && it.number === sel} context={g.id === 'closed'} onclick={() => onpick(it)}>
          {#snippet lead()}{#if unresolvedExport}<span role="img" aria-label="Promoted">↗</span>{:else}<PRGlyph kind="issue" {it} />{/if}{/snippet}
          <div class="title">{it.title}</div>
          <div class="identity">{#if it.number}#{it.number}{:else}{it.local?.id}{/if}{#if it.author?.login} <span>by {it.author.login}</span>{/if}<span>{ago(it.updatedAt)}</span>{#if it.commentCount}<span>{it.commentCount} {it.commentCount === 1 ? 'comment' : 'comments'}</span>{/if}</div>
          <div class="facts">
            <Chip outline>{it.source || 'GitHub'}</Chip>
            {#if it.local && location(it.local)}<span>{location(it.local)}</span>{/if}
            {#if chip}<Chip tone={chip.tone} outline={!chip.tone}>{chip.label}</Chip>{/if}
            {#each (it.labels ?? []).slice(0, 3) as l}<span class="label" style={labelStyle(l.color)}>{l.name}</span>{/each}
            {#if open && g.id !== 'has-pr'}<span class="pr">PR #{open.number}{open.isDraft ? ' draft' : ''}</span>{/if}
          </div>
        </Row>
        {#if it.local?.githubUrl}<a class="start" href={it.local.githubUrl} target="_blank" rel="noreferrer">GitHub ↗</a>{/if}
        {#if onmention}<button class="start" onclick={() => onmention?.(issueReference(it))}>Mention in chat</button>{/if}
        {#if it.local?.state === 'open' && (onexport || oncloseissue || ondismiss)}
          <div class="actions">
            {#if onexport}<button class="start" onclick={() => onexport?.(it)}>Promote</button>{/if}
            {#if oncloseissue}<button class="start" onclick={() => oncloseissue?.(it)}>Close</button>{/if}
            {#if ondismiss}<button class="start" onclick={() => ondismiss?.(it)}>Dismiss</button>{/if}
          </div>
        {:else if !onmention && !!it.number && it.group === 'needs-owner'}<button class="start" disabled={starting === it.number} onclick={() => onstart(it)} title="Hand this issue to an agent">Start</button>{/if}
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
  a.start { text-decoration: none; }
  .actions { display: flex; flex-wrap: wrap; gap: 4px; justify-content: flex-end; max-width: 180px; }
  .start { flex: none; font-size: 12.5px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 5px 12px; }
  .start:hover:not(:disabled) { color: var(--ink); background: var(--raise-hover); }
  .start:disabled { opacity: 0.5; }
  @media (max-width: 760px) { .item { flex-wrap: wrap; padding-bottom: var(--s2); } .item :global(.row) { flex-basis: 100%; } .actions { max-width: none; } .rows { padding-left: var(--s4); padding-right: var(--s4); } .start { min-height: 44px; padding: 0 14px; } .pr { margin-left: 0; } }
</style>
