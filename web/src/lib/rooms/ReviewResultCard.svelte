<script lang="ts">
  // The card for one reviewer turn's typed result. It shows the verdict, the
  // findings by severity, or a stop reason. It reads the
  // typed payload, never the review text, and keeps no feed-only state. The
  // feed and a session DM draw the same card.
  import { openExternal } from '../../api.ts'
  import Chip from '../ui/Chip.svelte'
  import EventRow from './EventRow.svelte'
  import ReviewIcon from '../ui/ReviewIcon.svelte'
  import EventDetails from './EventDetails.svelte'
  import { reviewEvent } from './eventPayloads.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { ReviewKind, ReviewSeverity } from './events.gen.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<ReviewKind, string> = {
    approve: 'approved',
    changes: 'changes requested',
    'approve-fast': 'approved',
    'stop-early': 'stopped early',
  }
  const stopWords: Record<string, string> = {
    superseded: 'a newer review replaced this one',
    duplicate: 'this review is a duplicate',
    empty: 'the change is empty',
    'too-large': 'the change is too large to review',
  }
  const severityWords: Record<ReviewSeverity, string> = { blocking: 'blocking', 'should-fix': 'should fix', nit: 'nit' }
  let payload = $derived(reviewEvent(post))
  let kind = $derived<ReviewKind>(payload?.kind ?? 'approve')
  let fast = $derived(kind === 'approve-fast' && payload?.fastLand === true)
  let counts = $derived(payload ? [payload.blocking ?? 0, payload.shouldFix ?? 0, payload.nits ?? 0] : [0, 0, 0])
  let findings = $derived(payload?.findings ?? [])
  let tone = $derived(kind === 'approve-fast' || kind === 'approve' ? 'sage' : kind === 'stop-early' ? '' : (counts[0] ?? 0) > 0 ? 'coral' : 'gold')
  let title = $derived(payload?.label ?? 'Reviewer result')
  let reason = $derived(payload?.stopReason ? (stopWords[payload.stopReason] ?? payload.stopReason) : '')
  let reviewerReason = $derived(payload?.summary?.trim() ?? '')
  let nextStep = $derived(kind === 'stop-early' && payload?.stopReason === 'too-large' ? (payload.number ? 'This PR' : 'This change') + ' cannot merge until it is split or root records a review.' : '')
  let showAll = $state(false)
  let shown = $derived(showAll ? findings : findings.slice(0, 3))
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  chips={[{ label: kind === 'approve-fast' ? (fast ? 'approved for a fast landing' : 'approved') : stateWords[kind], tone, outline: tone === '' }]}
  summary={[kind === 'stop-early' && reason ? `Stopped early: ${reason}.` : '', findings.length ? `${findings.length} finding${findings.length === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}<ReviewIcon size={15} />{/snippet}
  {#snippet detail()}
  <div class="body">
    {#if payload}
      {#if kind === 'stop-early' && reviewerReason && reviewerReason !== `Stopped early: ${reason}.`}<p class="reason">{reviewerReason}</p>{/if}
      {#if nextStep}<p class="reason">{nextStep}</p>{/if}
      {#if findings.length}
        <div class="counts">
          {#if counts[0]}<Chip tone="danger">{counts[0]} blocking</Chip>{/if}
          {#if counts[1]}<Chip tone="warning">{counts[1]} should fix</Chip>{/if}
          {#if counts[2]}<Chip>{counts[2]} nit{counts[2] === 1 ? '' : 's'}</Chip>{/if}
        </div>
        <ul class="findings">
          {#each shown as finding (finding.id)}
            <li>
              <span class="where"><Chip tone={finding.severity === 'blocking' ? 'danger' : finding.severity === 'should-fix' ? 'warning' : 'neutral'}>{severityWords[finding.severity]}</Chip> <b class="mono">{finding.file}{finding.line ? `:${finding.line}` : ''}</b></span>
              <span class="why">{finding.why}</span>
            </li>
          {/each}
        </ul>
        {#if findings.length > 3}
          <button class="more" type="button" onclick={() => (showAll = !showAll)}>{showAll ? 'Show fewer' : `Show ${findings.length - 3} more`}</button>
        {/if}
      {/if}
      <div class="acts">
        {#if payload.location}<button class="btn" type="button" onclick={() => openExternal(payload?.location ?? '')}>Open {payload.number ? 'PR' : 'change'}</button>{/if}
      </div>
    {/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
  {/snippet}
</EventRow>

<style>
  .reason { margin: 0 0 var(--s2); color: var(--muted); font-size: var(--t-sm); }
  .counts { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: var(--s2); }
  .findings { list-style: none; margin: 0 0 var(--s2); padding: 0; display: grid; gap: 6px; }
  .findings li { display: grid; gap: 2px; min-width: 0; }
  .where { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: var(--t-sm); color: var(--ink-2); }
  .mono { font-family: var(--mono); font-size: var(--t-cap); overflow-wrap: anywhere; }
  .why { color: var(--muted); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .more, .btn { padding: 3px 8px; border: 1px solid var(--line); border-radius: var(--r-sm); background: none; color: var(--muted); font: inherit; font-size: var(--t-sm); cursor: pointer; transition: color var(--motion-fast) var(--ease); }
  .more { margin-bottom: var(--s2); }
  .more:hover, .btn:hover { color: var(--ink-2); }
  .acts { display: flex; gap: var(--s2); }
</style>
