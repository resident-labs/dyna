<script lang="ts">
  // A turn result uses the regular message layout: the session's avatar, name,
  // time and markdown report. Only the small footer marks the turn's state.
  import type { EventProps } from './eventRenderers.ts'
  import { turnEvent } from './eventPayloads.ts'
  import type { TurnState } from './events.gen.ts'
  import FeedMessage from './FeedMessage.svelte'
  import { short } from '../ui/format.ts'
  import { turnMessage } from './turnMessage.ts'

  let { post, surface = 'feed', room = undefined, onaddress = undefined }: EventProps = $props()

  const stateWords: Record<TurnState, string> = { completed: 'finished', failed: 'failed', stopped: 'stopped' }

  let payload = $derived(turnEvent(post))
  let state = $derived<TurnState>(payload?.state ?? 'completed')
  // The room feed replaces a completion's stored summary with the turn's full
  // result. Both forms lose the lead-in because the footer already names it.
  let result = $derived(turnMessage(post))
  // A failed turn's worker stderr is diagnostic evidence. It stays behind a
  // closed disclosure, never in the row: the cause the body shows is the
  // error, and inspection commands live in the agent-facing detail only.
  let workerStderr = $derived(state === 'failed' ? payload?.workerStderr ?? '' : '')
  let name = $derived(short(payload?.session ?? post.speaker ?? post.from ?? '', room?.name))
  let message = $derived({
    ...result,
    speaker: name,
    human: false,
    owner: (payload?.session ?? post.from) === room?.name,
  })
</script>

<FeedMessage post={message} room={room ?? { name: '' }} {surface} fullText={surface === 'dm'} turnResult {onaddress}>
  {#snippet footer()}
    {#if workerStderr}
      <details class="turn-stderr">
        <summary>Worker stderr</summary>
        <pre class="stderr-text">{workerStderr}</pre>
      </details>
    {/if}
    {#if payload?.candidates?.length || payload?.candidatesError}
      <section class="turn-candidates" aria-label="Transfer profiles">
        <strong>Transfer profiles</strong>
        <div>Transfer: <code>dyna subagent transfer {payload.sessionId} --profile &lt;profile&gt;</code></div>
        {#each payload.candidates ?? [] as candidate (candidate.profile)}
          <div>{candidate.profile}: {candidate.state === 'available' ? 'Available' : candidate.state === 'unavailable' ? 'Unavailable' : 'Availability unknown'}{#if candidate.evidenceAt} · evidence {candidate.evidenceAt}{/if}</div>
        {/each}
        {#if payload.candidatesError}<div>Some availability evidence could not be read.</div>{/if}
      </section>
    {/if}
    <div class="turn-meta {state}">{stateWords[state]} · turn {payload?.turn ?? post.turn ?? '?'}</div>
  {/snippet}
</FeedMessage>

<style>
  .turn-candidates { margin-top: var(--s1); color: var(--muted); font-size: var(--t-cap); }
  .turn-stderr { margin-top: var(--s1); }
  .turn-stderr summary { color: var(--muted); font-size: var(--t-cap); cursor: pointer; list-style: none; }
  .turn-stderr summary::-webkit-details-marker { display: none; }
  .turn-stderr summary:hover { text-decoration: underline; }
  .stderr-text { margin: var(--s1) 0 0; padding: var(--s1); max-height: 200px; overflow: auto; background: var(--main); border-radius: var(--r-sm); color: var(--ink-2); white-space: pre-wrap; overflow-wrap: anywhere; font: var(--t-sm)/1.5 var(--mono); }
  .turn-meta { margin-top: var(--s1); color: var(--faint); font-size: var(--t-cap); }
  .turn-meta.failed { color: var(--coral); }
  .turn-meta.stopped { color: var(--gold); }
</style>
