<script lang="ts">
  // The card for a hook run: one quiet line in the feed and in a session DM,
  // with the state of the run, how long it took and a way to its log. It reads
  // the typed payload of the post, never its text, and keeps no feed-only
  // state. The start of a run folds into its result, and repeated passes of one
  // hook fold into one row: the card then says how many passes it holds and
  // when the last one ran, and View log opens that last run.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { hookEvent } from './eventPayloads.ts'
  import type { HookState } from './events.gen.ts'
  import type { HookFold } from './feedRows.ts'
  import { openHookLog } from '../settings/hookLog.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import HookIcon from '../ui/HookIcon.svelte'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed', hookFold }: {
    post: EventPost
    notice?: EventNotice
    surface?: EventSurface
    hookFold?: HookFold
  } = $props()

  const stateWords: Record<HookState, string> = {
    running: 'running',
    passed: 'passed',
    failed: 'failed',
    error: 'error',
  }
  // A failed hook and a hook that could not run both need the owner's eye.
  const stateTone: Record<HookState, string> = {
    running: 'gold',
    passed: 'sage',
    failed: 'coral',
    error: 'coral',
  }

  let payload = $derived(hookEvent(post))
  let name = $derived(payload?.label || payload?.hook || 'Hook')
  // A folded row shows the state of its newest post. A run of the hook that
  // started and has no result yet keeps the row running, also when a newer run
  // of the same hook already passed: a manual run and a scheduled run can be
  // live together.
  let state = $derived<HookState>(hookFold?.active ? 'running' : payload?.state ?? 'running')
  // View log opens the newest run of the row, or the run that is still going.
  let runId = $derived(hookFold?.active?.runId ?? payload?.runId ?? '')
  // A folded row counts its passes here; a row of one run shows only its own
  // time, so a pass that folded once does not claim a count.
  let count = $derived(hookFold?.count ?? 1)
  // A run under a second reads in milliseconds, so a quick hook does not look
  // like a clock that failed to move. A fold shows the time of its last pass
  // instead.
  let elapsed = $derived.by(() => {
    const ms = payload?.durationMs ?? 0
    if (ms <= 0) return ''
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
  })

  function viewLog() {
    if (!payload || !runId) return
    openHookLog({ repo: payload.repo, hook: payload.hook, runId })
  }
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={name}
  chips={[...(payload?.builtin ? [{ label: 'Automatic', tone: 'iris' }] : []), { label: stateWords[state], tone: stateTone[state], failure: state === 'failed' || state === 'error' }]}
  summary={[count > 1 && hookFold ? `${count} times · last ${clock(hookFold.lastTs)}` : elapsed, payload?.detail ?? ''].filter(Boolean).join(' · ')}
>
  {#snippet icon()}<HookIcon size={15} />{/snippet}
  {#snippet detail()}
    {#if payload?.detail}<p class="detail-text">{payload.detail}</p>{/if}
    {#if runId}<button class="link" type="button" onclick={viewLog} aria-label={count > 1 ? `View the log of the last run of ${name}` : `View the log of ${name}`}>View log</button>{/if}
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .detail-text { margin: 0 0 var(--s2); color: var(--muted); overflow-wrap: anywhere; }
</style>
