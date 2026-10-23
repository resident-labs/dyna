<script lang="ts">
  // The card for a merge trial and for root's recorded decision. One card folds
  // every event of a trial: what is merging, the trial's progress, its checks
  // and the commit it landed. It reads the typed payload, never the text, and
  // keeps no feed-only state.
  import { openExternal } from '../../api.ts'
  import { clock } from '../ui/format.ts'
  import Chip from '../ui/Chip.svelte'
  import EventDetails from './EventDetails.svelte'
  import { mergeDecision, mergeEvent } from './eventPayloads.ts'
  import { shortSha, commitUrl } from '../prs/pr.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { MergeStep } from './events.gen.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  // The four steps the progress bar shows, in order.
  const steps: readonly MergeStep[] = ['queued', 'trial', 'checks', 'landed']
  const stepWords: Record<MergeStep, string> = {
    queued: 'queued',
    trial: 'trial running',
    checks: 'checks running',
    landed: 'landed',
    failed: 'failed',
    returned: 'returned by root',
    conflict: 'conflict',
    'rolled-back': 'rolled back',
    decision: 'decision',
  }
  // A landed trial is sage, a running one gold, and an ending that needs a
  // person coral.
  const stepTones: Record<MergeStep, '' | 'gold' | 'sage' | 'coral'> = {
    queued: 'gold',
    trial: 'gold',
    checks: 'gold',
    landed: 'sage',
    failed: 'coral',
    returned: 'coral',
    conflict: 'coral',
    'rolled-back': 'coral',
    decision: '',
  }
  const verbs: Record<MergeStep, string> = {
    queued: 'Merging',
    trial: 'Merging',
    checks: 'Merging',
    landed: 'Merged',
    failed: 'Merge failed:',
    returned: 'Returned by root:',
    conflict: 'Merge conflict:',
    'rolled-back': 'Rolled back:',
    decision: 'Merge decision',
  }
  // The progress bar says how far the trial got. A failure or a return stops
  // at the step the steward had reached, and marks that segment coral.
  const progress: Record<MergeStep, readonly ('done' | 'live' | 'bad' | 'pending')[]> = {
    queued: ['live', 'pending', 'pending', 'pending'],
    trial: ['done', 'live', 'pending', 'pending'],
    checks: ['done', 'done', 'live', 'pending'],
    landed: ['done', 'done', 'done', 'done'],
    failed: ['done', 'done', 'done', 'bad'],
    returned: ['done', 'bad', 'pending', 'pending'],
    conflict: ['done', 'bad', 'pending', 'pending'],
    'rolled-back': ['done', 'done', 'done', 'bad'],
    decision: [],
  }

  let payload = $derived(mergeEvent(post))
  let decision = $derived(mergeDecision(post))
  let type = $derived<MergeStep>(payload?.type ?? 'decision')
  let prs = $derived(payload?.prs ?? [])
  let first = $derived(prs[0])
  // A local change has no PR number, so the card names it by its title.
  let name = $derived(first ? (first.number ? `#${first.number}` : first.title || 'the change') : '')
  let title = $derived(type === 'decision' ? verbs.decision : `${verbs[type]} ${name} into ${payload?.base ?? ''}`)
  let failedChecks = $derived(payload?.checks?.failed ?? [])
  let openCommit = $derived(commitUrl(first?.url, payload?.commit))
  let open = $state(false)

  function openLink(url?: string) {
    if (url) openExternal(url)
  }
</script>

<div class="card {surface}" class:open data-id={post.id}>
  <button class="sum" type="button" aria-expanded={open} aria-label={title} onclick={() => (open = !open)}>
    <span class="glyph" class:done={type === 'landed'} class:bad={stepTones[type] === 'coral'} aria-hidden="true">⇢</span>
    <span class="title">
      <span class="what">{title}</span>
      {#if type !== 'decision'}<Chip tone={stepTones[type]}>{stepWords[type]}</Chip>{/if}
    </span>
    <time title={new Date(post.ts ?? 0).toLocaleString()}>{clock(post.ts)}</time>
  </button>
  <div class="body">
    {#if decision}<p class="note">{decision.note}</p>{/if}
    {#if payload}
      {#if first?.title}<p class="sub">{first.title}</p>{/if}
      <div class="steps" aria-hidden="true">
        {#each progress[type] as segment}<i class={segment}></i>{/each}
      </div>
      {#if type !== 'decision'}
        <div class="legend" aria-hidden="true">
          {#each steps as name, i}<span class={progress[type][i]}>{name}</span>{/each}
        </div>
      {/if}
      <div class="facts">
        {#if prs.length > 1}<span>{prs.length} PRs</span>{/if}
        {#if payload.checks && (payload.checks.total ?? 0) > 0}<span>checks <b>{payload.checks.done}/{payload.checks.total}</b></span>{/if}
        {#if payload.commit}<span>commit <b class="mono">{shortSha(payload.commit)}</b></span>{/if}
      </div>
      {#if payload.reason && stepTones[type] === 'coral'}<p class="reason">{payload.reason}</p>{/if}
      {#if failedChecks.length}<p class="reason">Failed checks: {failedChecks.join(', ')}</p>{/if}
      <div class="acts">
        {#if first?.url}<button class="btn" type="button" onclick={() => openLink(first?.url)}>Open PR</button>{/if}
        {#if openCommit}<button class="btn" type="button" onclick={() => openLink(openCommit)}>Open commit</button>{/if}
      </div>
    {/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
    min-width: 0;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: 0;
    background: none;
    color: var(--ink-2);
    font: inherit;
    text-align: left;
    /* The card is always open on a desktop, so only the phone taps to expand. */
    pointer-events: none;
  }
  .glyph { flex: none; color: var(--gold); font-size: var(--t-ui); }
  .glyph.done { color: var(--sage); }
  .glyph.bad { color: var(--coral); }
  .title { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .what { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fact { color: var(--muted); font-size: var(--t-sm); white-space: nowrap; flex: none; }
  .note { margin: 0; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  /* The clock keeps the row short; the full stamp is in the title. */
  time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; flex: none; padding-left: 4px; }
  .body { display: grid; gap: 6px; padding: 0 12px 10px; }
  .sub { margin: 0; color: var(--muted); font-size: var(--t-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .steps { display: grid; grid-template-columns: repeat(4, 1fr); gap: 3px; width: 100%; max-width: 360px; }
  .steps i { height: 3px; border-radius: 2px; background: var(--line); }
  .steps i.done { background: var(--sage); }
  .steps i.live { background: var(--gold); }
  .steps i.bad { background: var(--coral); }
  .legend { display: grid; grid-template-columns: repeat(4, 1fr); gap: 3px; width: 100%; max-width: 360px; color: var(--faint); font-size: var(--t-cap); }
  .legend .done { color: var(--sage); }
  .legend .live { color: var(--gold); }
  .legend .bad { color: var(--coral); }
  .facts { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; color: var(--muted); font-size: var(--t-sm); }
  .facts b { color: var(--ink-2); font-weight: 500; }
  .mono { font-family: var(--mono); }
  .reason { margin: 0; color: var(--coral); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .btn {
    min-height: 28px;
    padding: 3px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-sm);
  }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); cursor: pointer; pointer-events: auto; }
    /* One row on a phone: the title, the step and the clock; the rest expands. */
    .title .fact { display: none; }
    .legend { display: none; }
    time { display: none; }
    .card:not(.open) .body { display: none; }
    .btn { min-height: 36px; }
  }
</style>
