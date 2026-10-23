<script lang="ts">
  // The card for one reviewer turn's typed result. It shows the verdict, the
  // findings by severity, a stop reason, or the fast-landing note. It reads the
  // typed payload, never the review text, and keeps no feed-only state. The
  // feed and a session DM draw the same card.
  import { openExternal } from '../../api.ts'
  import { clock } from '../ui/format.ts'
  import Chip from '../ui/Chip.svelte'
  import ReviewIcon from '../ui/ReviewIcon.svelte'
  import EventDetails from './EventDetails.svelte'
  import { reviewEvent } from './eventPayloads.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { ReviewKind, ReviewSeverity } from './events.gen.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const words: Record<ReviewKind, string> = {
    approve: 'approved',
    changes: 'changes requested',
    'approve-fast': 'approved for a fast landing',
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
  let title = $derived(payload ? `${payload.label} ${kind === 'approve-fast' && !fast ? 'approved' : words[kind]}` : 'Reviewer result')
  let note = $derived(kind === 'approve-fast' && !fast ? 'normal path' : (payload?.note ?? ''))
  let reason = $derived(payload?.stopReason ? (stopWords[payload.stopReason] ?? payload.stopReason) : '')
  let open = $state(false)
  let showAll = $state(false)
  let shown = $derived(showAll ? findings : findings.slice(0, 3))
</script>

<div class="card {surface}" class:open>
  <button class="sum" type="button" aria-expanded={open} aria-label={title} onclick={() => (open = !open)}>
    <span class="glyph" class:done={tone === 'sage'} class:bad={tone === 'coral'}><ReviewIcon size={15} /></span>
    <span class="title">
      <span class="what">{title}</span>
      <Chip {tone} outline={tone === ''}>{kind === 'approve-fast' ? (fast ? 'fast landing' : 'approved') : kind === 'changes' ? 'changes' : kind === 'stop-early' ? 'stopped' : 'approved'}</Chip>
    </span>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </button>
  <div class="body">
    {#if payload}
      {#if note}<p class="note">{note}</p>{/if}
      {#if kind === 'stop-early' && reason}<p class="reason">Stopped early: {reason}.</p>{/if}
      {#if findings.length}
        <div class="counts">
          {#if counts[0]}<Chip tone="coral">{counts[0]} blocking</Chip>{/if}
          {#if counts[1]}<Chip tone="gold">{counts[1]} should fix</Chip>{/if}
          {#if counts[2]}<Chip>{counts[2]} nit{counts[2] === 1 ? '' : 's'}</Chip>{/if}
        </div>
        <ul class="findings">
          {#each shown as finding (finding.id)}
            <li>
              <span class="where"><Chip tone={finding.severity === 'blocking' ? 'coral' : finding.severity === 'should-fix' ? 'gold' : ''}>{severityWords[finding.severity]}</Chip> <b class="mono">{finding.file}{finding.line ? `:${finding.line}` : ''}</b></span>
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
</div>

{#snippet finderNote(value: string, isFast: boolean)}
  {#if value && !isFast && false}<span>{value}</span>{/if}
{/snippet}

<style>
  .card { margin: var(--s1) var(--s4); border: 1px solid var(--line); background: var(--side); border-radius: 8px; overflow: hidden; min-width: 0; }
  .card.dm { margin: var(--s1) 0; }
  .sum { display: flex; align-items: center; gap: 8px; width: 100%; padding: 8px 12px; border: 0; background: none; color: inherit; font: inherit; text-align: left; cursor: pointer; }
  .glyph { display: inline-flex; flex: none; color: var(--muted); }
  .glyph.done { color: var(--sage); }
  .glyph.bad { color: var(--coral); }
  .title { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; }
  .what { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: var(--t-sm); }
  time { flex: none; color: var(--faint); font-size: var(--t-cap); }
  .body { display: none; padding: 0 12px 10px; }
  .card.open .body { display: block; }
  .note { margin: 0 0 var(--s2); color: var(--muted); font-size: var(--t-sm); }
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
