<script lang="ts">
  // The card of a detached job on a node: its command, the machine it runs on,
  // how it ended and how long it took. The command's output sits behind the
  // card disclosure and in `dyna job logs`. It reads the typed payload, never
  // the post text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { jobEvent } from './eventPayloads.ts'
  import { durationMs } from '../ui/format.ts'
  import type { JobState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<JobState, string> = {
    queued: 'waiting',
    running: 'running',
    passed: 'passed',
    failed: 'failed',
    cancelled: 'cancelled',
    lost: 'lost',
    timed_out: 'timed out',
  }
  const stateTone: Record<JobState, string> = {
    queued: 'gold',
    running: 'sky',
    passed: 'sage',
    failed: 'coral',
    cancelled: 'gold',
    lost: 'gold',
    timed_out: 'gold',
  }

  let payload = $derived(jobEvent(post))
  let state = $derived<JobState>(payload?.state ?? 'running')
  let label = $derived(payload?.label || 'Job')
  let node = $derived(payload?.node || '')
  let chips = $derived([{ label: stateWords[state], tone: stateTone[state], failure: state === 'failed' }])
  let elapsed = $derived(durationMs(payload?.durationMs ?? 0))
  let exit = $derived(payload && payload.exitCode !== undefined && payload.exitCode >= 0 && payload.state !== 'running' && payload.state !== 'queued' ? `exit ${payload.exitCode}` : '')
  let outcome = $derived(payload && payload.exitCode === undefined ? (payload.outcome ?? '') : '')
  let summary = $derived([node, exit || outcome, elapsed].filter(Boolean).join(' · '))
  let hasBody = $derived(!!payload?.detail || !!payload?.waitReason)
</script>

{#snippet icon()}
  <span class="glyph">▸</span>
{/snippet}

{#snippet body()}
  {#if payload?.waitReason}<p class="text">{payload.waitReason}</p>{/if}
  {#if payload?.detail}<p class="text">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title={label} {chips} {summary} {icon} detail={hasBody ? body : undefined} />

{#if surface === 'dm' && !hasBody}<EventDetails text={post.text} />{/if}

<style>
  .glyph { display: inline-block; }
  .text { margin: 0; overflow-wrap: anywhere; }
</style>
