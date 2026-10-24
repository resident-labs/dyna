<script lang="ts">
  // The room's local and GitHub issues, one row each in the PR list style. A row has the state icon, the title, the number,
  // author, age, one state chip, labels and the linked PR.
  // The row ends with one compact action group (RowActions): Mention in chat
  // as an icon, and Promote, Close, Dismiss and GitHub in its menu.
  //
  // Props:
  //   groups    the issueGroups answer
  //   sel       the open issue's number
  //   onpick    opens an issue; onmention adds it to the composer
  import Row from '../ui/Row.svelte'
  import Chip from '../ui/Chip.svelte'
  import PRGlyph from './PRGlyph.svelte'
  import { openExternal } from '../../api.ts'
  import { ago } from '../ui/format.ts'
  import { labelStyle, issueChip } from './github.ts'
  import { issueReference, issueWork, roomIssueLabel, type IssueReference, type IssueRow as GitHubItem } from './unifiedIssues.ts'
  import RowActions from './RowActions.svelte'
  import type { RowAction } from './props.ts'
  import { location } from '../issues/localIssues.ts'

  interface Props {
    groups?: { id: string; label: string; rows: GitHubItem[] }[]
    sel?: number
    onpick: (item: GitHubItem) => void
    onmention?: (reference: IssueReference) => void
    oncloseissue?: (item: GitHubItem) => void
    onexport?: (item: GitHubItem) => void
    ondismiss?: (item: GitHubItem) => void
  }
  let { groups = [], sel = 0, onpick, onmention, oncloseissue, onexport, ondismiss }: Props = $props()

  // The row's name in a tooltip or a menu title: the number Dyna shows.
  function rowName(it: GitHubItem): string {
    return it.number ? `#${it.number}` : it.local ? roomIssueLabel(it.local) : 'this issue'
  }
  // Everything a row can do besides mention it, in the order the menu shows.
  function menuItems(it: GitHubItem): RowAction[] {
    const out: RowAction[] = []
    // A local issue leaves for GitHub once it is promoted; a GitHub issue has
    // its own page there.
    const url = it.local?.githubUrl || (it.source === 'GitHub' ? it.url : '')
    if (url) out.push({ label: 'Open on GitHub', run: () => openExternal(url) })
    if (it.local?.state !== 'open') return out
    if (onexport) out.push({ label: 'Promote', run: () => onexport?.(it) })
    if (oncloseissue) out.push({ label: 'Close', run: () => oncloseissue?.(it) })
    if (ondismiss) out.push({ label: 'Dismiss', run: () => ondismiss?.(it), danger: true, gap: true })
    return out
  }
</script>

<div class="rows">
  {#each groups as g (g.id)}
    {#each g.rows as it (it.number || it.local?.id)}
      {@const unresolvedExport = it.state === 'UNKNOWN' && !!it.local?.githubUrl}
      {@const chip = unresolvedExport ? { tone: '', label: 'Promoted' } : it.local?.inProgress ? { tone: 'iris', label: 'In progress' } : issueChip(it)}
      {@const work = issueWork(it)}
      {@const open = it.linkedPRs?.find((p) => p.state === 'OPEN') ?? it.linkedPRs?.[0]}
      <div class="item">
        <Row selected={!!it.number && it.number === sel} context={g.id === 'closed'} onclick={() => onpick(it)}>
          {#snippet lead()}{#if unresolvedExport}<span role="img" aria-label="Promoted">↗</span>{:else}<PRGlyph kind="issue" {it} />{/if}{/snippet}
          <div class="title">{it.title}</div>
          <div class="identity">{#if it.number}#{it.number}{:else if it.local}{roomIssueLabel(it.local)}{/if}{#if it.author?.login} <span>by {it.author.login}</span>{/if}<span>{ago(it.updatedAt)}</span>{#if it.commentCount}<span>{it.commentCount} {it.commentCount === 1 ? 'comment' : 'comments'}</span>{/if}</div>
          <div class="facts">
            <Chip outline>{it.source || 'GitHub'}</Chip>
            {#if it.local && location(it.local)}<span>{location(it.local)}</span>{/if}
            {#if chip}<Chip tone={chip.tone} outline={!chip.tone}>{chip.label}</Chip>{/if}
            {#if it.local?.goalShared}<span>Held by the room</span>{:else if it.local?.goalHolder}<span>Held by {it.local.goalHolder}</span>{/if}
            {#each (it.labels ?? []).slice(0, 3) as l}<span class="label" style={labelStyle(l.color)}>{l.name}</span>{/each}
            {#if open && g.id !== 'has-pr'}<span class="pr">PR #{open.number}{open.isDraft ? ' draft' : ''}</span>{:else if it.fixPR}<span class="pr">Fix in #{it.fixPR}</span>{:else if work === 'in-progress'}<span class="pr">no PR yet</span>{/if}
          </div>
        </Row>
        <RowActions name={rowName(it)} onmention={onmention ? () => onmention?.(issueReference(it)) : null} items={menuItems(it)} />
      </div>
    {/each}
  {/each}
</div>

<style>
  .rows { flex: 1; overflow-y: auto; padding: 0 var(--s5) var(--s4); }
  /* The actions appear when the row is hovered or focused. A phone has no
     hover, so it shows them all the time, still compact. */
  .item { display: flex; align-items: center; gap: var(--s2); border-bottom: 1px solid var(--line-soft); --row-actions-visibility: hidden; }
  .item:hover, .item:focus-within { --row-actions-visibility: visible; }
  @media (hover: none) { .item { --row-actions-visibility: visible; } }
  .item :global(.row) { flex: 1; min-width: 0; border-radius: 0; padding: var(--s4) 0; }
  .title { font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .identity { margin-top: var(--s1); color: var(--muted); font-size: var(--t-sm); display: flex; gap: var(--s2); flex-wrap: wrap; }
  .facts { display: flex; align-items: center; flex-wrap: wrap; gap: var(--s2); margin-top: var(--s2); }
  .facts:empty { display: none; }
  .pr { margin-left: auto; color: var(--muted); font-size: var(--t-sm); }
  .label { font-size: 11px; font-weight: 600; padding: 0 7px; border-radius: 9px; line-height: 17px; }
  @media (max-width: 760px) { .rows { padding-left: var(--s4); padding-right: var(--s4); } .pr { margin-left: 0; } }
</style>
