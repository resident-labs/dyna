<script lang="ts">
  // The row for a workflow run's result: its name, how it ended, how many
  // agent calls served it, and how long it took. The row carries the counts and
  // the time; the run's own result line and the full output sit behind the row
  // disclosure and in the run view. It reads the typed payload, never the post
  // text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { runEvent } from './eventPayloads.ts'
  import { durationMs } from '../ui/format.ts'
  import type { RunState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'
  import RunIcon from '../workflows/RunIcon.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const stateWords: Record<RunState, string> = { completed: 'done', failed: 'failed', stopped: 'stopped', interrupted: 'interrupted' }
  const stateTone: Record<RunState, string> = { completed: 'sage', failed: 'coral', stopped: 'gold', interrupted: 'gold' }

  let payload = $derived(runEvent(post))
  let state = $derived<RunState>(payload?.state ?? 'completed')
  let name = $derived(payload?.name || 'Workflow')
  let chips = $derived([{ label: stateWords[state], tone: stateTone[state], failure: state === 'failed' }])
  let elapsed = $derived(durationMs(payload?.durationMs ?? 0))
  let counts = $derived.by(() => {
    const steps = payload?.steps ?? 0
    const agents = payload?.agents ?? 0
    if (!steps) return ''
    const call = steps === 1 ? '1 agent call' : `${steps} agent calls`
    return agents > 1 ? `${call} · ${agents} agents` : call
  })
  let summary = $derived([counts, elapsed].filter(Boolean).join(' · '))
  let hasBody = $derived(!!payload?.summary)
</script>

{#snippet icon()}
  <RunIcon size={15} />
{/snippet}

{#snippet body()}
  {#if payload?.summary}<p class="text">{payload.summary}</p>{/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
{/snippet}

<EventRow id={post.id} ts={post.ts} {surface} title={name} {chips} {summary} {icon} detail={hasBody ? body : undefined} />

{#if surface === 'dm' && !hasBody}<EventDetails text={post.text} />{/if}

<style>
  .text { margin: 0; overflow-wrap: anywhere; }
</style>
