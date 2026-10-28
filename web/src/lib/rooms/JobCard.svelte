<script lang="ts">
  // The card of a detached job on a node: its command, the machine it runs on,
  // how it ended and how long it took. The command's output sits behind the
  // card disclosure and in `dyna job logs`. It reads the typed payload, never
  // the post text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { jobEvent } from './eventPayloads.ts'
  import type { JobState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<JobState, string> = {
    running: 'running',
    passed: 'passed',
    failed: 'failed',
    cancelled: 'cancelled',
    lost: 'lost',
    timed_out: 'timed out',
  }
  const stateTone: Record<JobState, string> = {
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
  // A job under a second reads in milliseconds, so a quick job does not look
  // like a clock that failed to move.
  let elapsed = $derived.by(() => {
    const ms = payload?.durationMs ?? 0
    if (ms <= 0) return ''
    if (ms < 1000) return `${ms} ms`
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
    return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
  })
  let exit = $derived(payload && payload.exitCode !== undefined && payload.state !== 'running' ? `exit ${payload.exitCode}` : '')
  let summary = $derived([node, exit, elapsed].filter(Boolean).join(' · '))
  let hasBody = $derived(!!payload?.detail)
</script>

{#snippet icon()}
  <span class="glyph">▸</span>
{/snippet}

{#snippet body()}
  {#if payload?.detail}<p class="text">{payload.detail}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title={label} {chips} {summary} {icon} detail={hasBody ? body : undefined} />

{#if surface === 'dm' && !hasBody}<EventDetails text={post.text} />{/if}

<style>
  .glyph { display: inline-block; }
  .text { margin: 0; overflow-wrap: anywhere; }
</style>
