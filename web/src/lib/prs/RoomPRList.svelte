<script lang="ts">
  import Row from '../ui/Row.svelte'
  import PRGlyph from './PRGlyph.svelte'
  import Chip from '../ui/Chip.svelte'
  import LoadOlder from '../ui/LoadOlder.svelte'
  import ExpediteBadge from '../rooms/ExpediteBadge.svelte'
  import { expedite, prExpedited } from '../rooms/expedite.ts'
  import { changeTitle } from './pr.ts'
  import { authorName, mergeState, type Row as ChangeRow, type Names } from './prlist.ts'
  import { ciLabel, reviewLabel, mergePosition } from './roomPRs.ts'
  import type { MergeQueueItem } from '../api/change.ts'
  let { rows, queue = [], names = {}, selId = '', older = false, loadingOlder = false, failedOlder = false, onolder = () => {}, onpick }: {
    rows: ChangeRow[]; queue?: MergeQueueItem[]; names?: Names; selId?: string; older?: boolean; loadingOlder?: boolean; failedOlder?: boolean; onolder?: () => void; onpick: (row: ChangeRow) => void
  } = $props()
</script>
<div class="rows">
  {#each rows as row (row.changeId)}
    {@const ci = ciLabel(row)}{@const review = reviewLabel(row)}
    {@const item = queue.find((q) => q.changeId === row.changeId)}
    {@const state = mergeState(row, item)}
    <Row selected={row.changeId === selId} onclick={() => onpick(row)}>
      {#snippet lead()}<PRGlyph it={row} />{/snippet}
      <div class="title">{changeTitle(row)} {#if row.isDraft}<Chip>Draft</Chip>{/if} {#if prExpedited($expedite, row)}<ExpediteBadge />{/if}</div>
      <div class="identity">#{row.number}{#if authorName(row, names)} <span>by {authorName(row, names)}</span>{/if}</div>
      <div class="facts"><Chip tone={ci.tone}>{ci.label}</Chip><Chip tone={review.tone} title={row.reviewBlocked}>{review.label}</Chip><span class="position">{mergePosition(row.changeId ?? '', queue)}</span></div>
      {#if item && !item.trial && item.state !== 'ready'}<div class="reason" title={state.reason}>{state.reason || state.label}</div>{/if}
    </Row>
  {/each}
  <LoadOlder auto {older} loading={loadingOlder} failed={failedOlder} onclick={onolder} />
</div>
<style>
  .rows { padding:0 var(--s5) var(--s4); }
  .rows :global(.row) { border-bottom:1px solid var(--line-soft); border-radius:0; padding:var(--s4) 0; }
  .title { font-size:var(--t-body); font-weight:600; overflow-wrap:anywhere; }
  .identity { margin-top:var(--s1); color:var(--muted); font-size:var(--t-sm); display:flex; gap:var(--s2); flex-wrap:wrap; }
  .facts { display:flex; align-items:center; flex-wrap:wrap; gap:var(--s2); margin-top:var(--s2); }
  .position { margin-left:auto; color:var(--muted); font-size:var(--t-sm); }
  .reason { margin-top:var(--s2); font-size:var(--t-sm); color:var(--muted); overflow-wrap:anywhere; }
  @media (max-width:760px) { .rows { padding-left:var(--s4); padding-right:var(--s4); }.position { margin-left:0; } }
</style>
