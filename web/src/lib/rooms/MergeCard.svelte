<script lang="ts">
  // The card for a merge trial, for root's recorded decision and for the
  // owner's merge-fix request. One card folds every event of a trial: what is
  // merging, the trial's progress, its checks and the commit it landed. It
  // reads the typed payload, never the text, and keeps no feed-only state.
  import { openExternal } from '../../api.ts'
  import EventRow from './EventRow.svelte'
  import EventDetails from './EventDetails.svelte'
  import { mergeDecision, mergeEvent } from './eventPayloads.ts'
  import { clock } from '../ui/format.ts'
  import { shortSha, commitUrl } from '../prs/pr.ts'
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import type { MergeReasonKind, MergeStep } from './events.gen.ts'

  let { post, posts = [post], notice = post.notice, surface = 'feed' }: { post: EventPost; posts?: EventPost[]; notice?: EventNotice; surface?: EventSurface } = $props()

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
    'fix-requested': 'fix requested',
  }
  // A landed trial is sage, a running one gold, and an ending that needs a
  // person coral.
  const stepTones: Record<MergeStep, '' | 'gold' | 'sage' | 'coral'> = {
    queued: '',
    trial: '',
    checks: '',
    landed: '',
    failed: 'coral',
    returned: 'coral',
    conflict: 'coral',
    'rolled-back': 'coral',
    decision: '',
    'fix-requested': 'gold',
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
    'fix-requested': 'Merge fix requested for',
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
    // A request has no trial to show progress for.
    'fix-requested': [],
  }

  let payload = $derived(mergeEvent(post))
  let decision = $derived(mergeDecision(post))
  let type = $derived<MergeStep>(payload?.type ?? 'decision')
  let prs = $derived(payload?.prs ?? [])
  let first = $derived(prs[0])
  // A local change has no PR number, so the card names it by its title.
  let name = $derived(first ? (first.number ? `#${first.number}` : first.title || 'the change') : '')
  // A fact that names one member (a return, a conflict, a member's own
  // failure) is about that PR alone; a trial-wide fact names every member.
  let affected = $derived(payload?.member ? prs.find((pr) => pr.number === payload?.member) : undefined)
  let what = $derived(
    payload?.member
      ? affected?.number
        ? `#${affected.number}`
        : affected?.title || `#${payload.member}`
      : prs.length > 1
        ? `${prs.length} PRs: ${prs.map((pr) => (pr.number ? `#${pr.number}` : pr.title || 'the change')).join(', ')}`
        : name,
  )
  // The card opens the PR it names: the affected member when the fact is about
  // one, else the first member of the batch. An affected member the payload no
  // longer lists names its number alone, so the card never points at another PR.
  let subject = $derived(payload?.member ? affected : first)
  let title = $derived(type === 'decision' ? verbs.decision : `${verbs[type]} ${what} into ${payload?.base ?? ''}`)
  let failedChecks = $derived(payload?.checks?.failed ?? [])
  // The card says a cause once. The Go side words a cause the steward
  // generated as a typed kind and leaves its text empty, so the evidence list
  // and the reason line never repeat each other.
  let reason = $derived(reasonLine(payload?.reason, payload?.reasonKind, failedChecks.length))
  let checkSummary = $derived(reason || (failedChecks.length ? `${failedChecks.length} failed check${failedChecks.length === 1 ? '' : 's'}` : ''))
  let openCommit = $derived(commitUrl(first?.url, payload?.commit))

  // reasonLine words a cause from its kind when the payload keeps no text.
  function reasonLine(text: string | undefined, kind: MergeReasonKind | undefined, named: number) {
    if (text) return text
    if (kind === 'tested-head') return 'Landed without a trial: this head contains the base tip and passed its required CI.'
    if (kind === 'checks-failed' && !named) return 'Checks failed.'
    return ''
  }

  function openLink(url?: string) {
    if (url) openExternal(url)
  }
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  summaryWrap={type === 'failed' || type === 'landed'}
  chips={[{ label: stepWords[type], tone: stepTones[type], failure: stepTones[type] === 'coral' }]}
>
  {#snippet summaryContent()}
    {#if type === 'failed' && failedChecks.length}
      {#if subject?.title}{subject.title}{' · '}{/if}{#if reason && reason !== 'Checks failed'}{reason}{' · '}{/if}Failed checks:  {#each failedChecks as check, i}{#if i}, {/if}<span class="check-name">{check}</span>{/each}
    {:else}
      {[decision?.note, subject?.title, payload?.commit ? `commit ${shortSha(payload.commit)}` : '', checkSummary, payload?.checks && (payload.checks.total ?? 0) > 0 && type !== 'failed' ? `${payload.checks.done} of ${payload.checks.total} checks ran` : '', payload?.head ? `head ${shortSha(payload.head)}` : ''].filter(Boolean).join(' · ')}
    {/if}
  {/snippet}
  {#snippet icon()}⇢{/snippet}
  {#snippet footer()}
      {#if posts.length > 1}
        <details>
          <summary>Show steps ({posts.length})</summary>
          {#each posts as step (step.id)}
            {@const event = mergeEvent(step)}
            {#if event}<div class="step-note"><time>{clock(step.ts)}</time> · {stepWords[event.type]}</div>{/if}
          {/each}
        </details>
      {/if}
  {/snippet}
  {#snippet detail()}
  <div class="body">
    {#if decision}<p class="note">{decision.note}</p>{/if}
    {#if payload}
      {#if subject?.title}<p class="sub">{subject.title}</p>{/if}
      {#if type === 'fix-requested' && payload.actor}<p class="note">Requested by {payload.actor}.</p>{/if}
      {#if progress[type].length}
        <div class="steps" aria-hidden="true">
          {#each progress[type] as segment}<i class={segment}></i>{/each}
        </div>
        <div class="legend" aria-hidden="true">
          {#each steps as name, i}<span class={progress[type][i]}>{name}</span>{/each}
        </div>
      {/if}
      <div class="facts">
        {#if affected && prs.length > 1}<span>batch of {prs.length}</span>{/if}
        {#if payload.checks && (payload.checks.total ?? 0) > 0}<span><b>{payload.checks.done} of {payload.checks.total}</b> checks ran</span>{/if}
        {#if payload.head}<span>head <b class="mono">{shortSha(payload.head)}</b></span>{/if}
        {#if payload.commit}<span>commit <b class="mono">{shortSha(payload.commit)}</b></span>{/if}
      </div>
      {#if reason && stepTones[type] === 'coral'}<p class="reason">{reason}</p>{:else if reason && type === 'landed'}<p class="note">{reason}</p>{/if}
      {#if failedChecks.length}<p class="reason">Failed checks: {failedChecks.join(', ')}</p>{/if}
      <div class="acts">
        {#if subject?.url}<button class="btn" type="button" onclick={() => openLink(subject?.url)}>Open PR</button>{/if}
        {#if openCommit}<button class="btn" type="button" onclick={() => openLink(openCommit)}>Open commit</button>{/if}
      </div>
    {/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
  {/snippet}
</EventRow>

<style>
  .check-name { white-space: nowrap; }
  .step-note { color: var(--muted); }
  .note { margin: 0; color: var(--ink-2); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .body { display: grid; gap: 6px; }
  .sub { margin: 0; color: var(--muted); font-size: var(--t-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .steps { display: grid; grid-template-columns: repeat(4, 1fr); gap: 3px; width: 100%; max-width: 360px; }
  .steps i { height: 3px; border-radius: 2px; background: var(--line); }
  .steps i.done { background: var(--muted); }
  .steps i.live { background: var(--muted); }
  .steps i.bad { background: var(--coral); }
  .legend { display: grid; grid-template-columns: repeat(4, 1fr); gap: 3px; width: 100%; max-width: 360px; color: var(--faint); font-size: var(--t-cap); }
  .legend .done { color: var(--muted); }
  .legend .live { color: var(--muted); }
  .legend .bad { color: var(--coral); }
  .facts { display: flex; align-items: center; gap: var(--s2); flex-wrap: wrap; color: var(--muted); font-size: var(--t-sm); }
  .facts b { color: var(--ink-2); font-weight: 500; }
  .mono { font-family: var(--mono); }
  .reason { margin: 0; color: var(--coral); font-size: var(--t-sm); overflow-wrap: anywhere; }
  .acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  @media (max-width: 760px) {
    .legend { display: none; }
  }
</style>
