<script lang="ts">
  // A turn result uses the regular message layout: the session's avatar, name,
  // time and markdown report. Only the small footer marks the turn's state.
  import type { EventProps } from './eventRenderers.ts'
  import { turnEvent } from './eventPayloads.ts'
  import type { TurnState } from './events.gen.ts'
  import FeedMessage from './FeedMessage.svelte'
  import { short, turnText } from '../ui/format.ts'

  let { post, surface = 'feed', room = undefined, onaddress = undefined }: EventProps = $props()

  const stateWords: Record<TurnState, string> = { completed: 'finished', failed: 'failed', stopped: 'stopped' }

  let payload = $derived(turnEvent(post))
  let state = $derived<TurnState>(payload?.state ?? 'completed')
  // The room feed replaces a completion's stored summary with the turn's full
  // result. Both forms lose the lead-in because the footer already names it.
  let body = $derived(turnText(post.fullResult && post.text ? post.text : payload?.summary ?? ''))
  let error = $derived(payload?.error ?? '')
  // A failed turn keeps its evidence, such as the worker's stderr, in the
  // detail block — never in the cause the body shows.
  let detail = $derived(state === 'failed' ? payload?.detail ?? '' : '')
  let name = $derived(short(payload?.session ?? post.speaker ?? post.from ?? '', room?.name))
  let message = $derived({
    ...post,
    from: payload?.session ?? post.from,
    speaker: name,
    text: body || error,
    human: false,
    owner: (payload?.session ?? post.from) === room?.name,
  })
</script>

<FeedMessage post={message} room={room ?? { name: '' }} {surface} fullText={surface === 'dm'} turnResult {onaddress}>
  {#snippet footer()}
    {#if detail}<div class="turn-detail">{detail}</div>{/if}
    <div class="turn-meta {state}">{stateWords[state]} · turn {payload?.turn ?? post.turn ?? '?'}</div>
  {/snippet}
</FeedMessage>

<style>
  .turn-detail { margin-top: var(--s1); color: var(--muted); font-size: var(--t-sm); white-space: pre-line; overflow-wrap: anywhere; }
  .turn-meta { margin-top: var(--s1); color: var(--faint); font-size: var(--t-cap); }
  .turn-meta.failed { color: var(--coral); }
  .turn-meta.stopped { color: var(--gold); }
</style>
