<script lang="ts">
  // The one box that says where a change stands: three steps (CI, review,
  // merge), one plain sentence and one main button. It also holds a short
  // note when a bot asked for changes, a link to the rules, and the comment
  // filter line when the filter hid something.
  //
  // Props:
  //   change     a Changes row
  //   merge      its RoomMergeStatus item, if any
  //   review     whether automatic review is on in the room
  //   room       the room's policy, roomId the room
  //   onresume   resume a paused change; ontrack starts tracking
  //   onreview   jump to the review; onsession opens the reviewer's session
  //   onrules    open the rules (the review settings drawer)
  //   onchange   reload Changes after a land
  //   children   optional snippet under the sentence
  import type { Snippet } from 'svelte'
  import type { ChangePolicy } from '../api/change.ts'
  import { statusBox, isDone, isUntracked, type MergeItem, type Row } from './prlist.ts'
  import { commentFilterText, isLocal } from './pr.ts'
  import type { StatusActions } from './props.ts'
  import StepTrack from './StepTrack.svelte'
  import PRActions from './PRActions.svelte'

  interface Props extends StatusActions {
    change: Row
    merge?: MergeItem | null
    review?: boolean
    room?: ChangePolicy | null
    roomId?: string
    children?: Snippet | null
    onreview?: (() => void) | null
    onsession?: (() => void) | null
  }
  let { change, merge = null, review = true, room = null, roomId = '', children = null, onresume = null, ontrack = null, onreview = null, onsession = null, onrules = null, onchange = () => {} }: Props = $props()
  let box = $derived(statusBox(change, { merge, review }))
  let open = $derived(!isDone(change) && !isUntracked(change))
  let own = $derived(!!change.hasPolicyOverride)
</script>

<section class="status {change.state === 'merged' ? 'done' : ''}" aria-label="Status">
  {#if box.steps.length}<StepTrack steps={box.steps} />{/if}
  <p class="say">{box.say}</p>
  {#if box.note}<p class="bot">{box.note}</p>{/if}
  {#if children}{@render children()}{/if}
  {#if change.error}<p class="pollerr">Last check failed: {change.error}</p>{/if}
  {#if open && onrules}
    <button class="link" onclick={onrules}>{own ? 'Own rules for this PR' : 'Uses room rules'} · change for this {isLocal(change) ? 'change' : 'PR'}</button>
  {/if}
  {#if !isLocal(change) && (change.filteredComments ?? 0) > 0}
    <p class="filter" title="Counts the latest saved filter decision for each comment or review. Delivered updates do not reset this count.">{commentFilterText(change)}</p>
  {/if}
  <PRActions {change} {box} {onresume} {ontrack} {onreview} {onsession} />
</section>

<style>
  .status { border: 1px solid var(--line-soft); border-radius: 10px; padding: 14px 16px; background: var(--side); margin: 0 0 4px; }
  .say { margin: 12px 0 0; font-size: 14px; color: var(--ink); }
  .status > .say:first-child { margin-top: 0; }
  .bot { margin: 8px 0 0; font-size: 13px; color: var(--gold); }
  .pollerr { margin: 8px 0 0; font-size: 12.5px; color: var(--coral); }
  .link { display: block; margin-top: 10px; color: var(--iris); font-size: 12.5px; font-weight: 600; padding: 0; text-align: left; }
  .link:hover { text-decoration: underline; }
  .filter { margin: 10px 0 0; font-size: 12.5px; color: var(--faint); }
  @media (max-width: 760px) { .link { min-height: 44px; } }
</style>
