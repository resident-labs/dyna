<script lang="ts">
  // The card for a CI health line: the tests that newly crossed the CI time
  // limit, each with its time against the limit and the run it came from. It is
  // a quiet owner-feed card and reads the typed payload, never the text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { slowTestEvent } from './eventPayloads.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  let payload = $derived(slowTestEvent(post))
  let crossings = $derived(payload?.crossings ?? [])
  let title = $derived.by(() => {
    const count = payload?.count ?? 0
    const run = payload?.run ?? ''
    const tests = count === 1 ? '1 slow test' : `${count} slow tests`
    return run ? `${tests} over the CI limit · ${run}` : `${tests} over the CI limit`
  })

  // A run under a second reads in milliseconds, so a quick test does not look
  // like a clock that failed to move.
  const elapsed = (ms: number): string => (ms <= 0 ? '' : ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(2)} s`)
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  {title}
  chips={[{ label: 'over limit', tone: 'coral' }]}
  summary={crossings.slice(0, 2).map((crossing) => crossing.test).join(' · ')}
>
  {#snippet icon()}⏱{/snippet}
  {#snippet detail()}
  {#if crossings.length}
    <ul class="tests">
      {#each crossings as crossing, i (i)}
        <li>
          <span class="name" title={crossing.test}>{crossing.test}</span>
          <span class="time">{elapsed(crossing.durationMs)} / {elapsed(crossing.limitMs)}</span>
          <span class="meta">{crossing.package}{#if crossing.features?.length}{crossing.package ? ' · ' : ''}{crossing.features.join(', ')}{/if}</span>
          <span class="runs">{crossing.run}{#if crossing.otherRun}{' was '}{crossing.otherRun}{/if}</span>
        </li>
      {/each}
    </ul>
  {:else}
    <!-- TODO: legacy/shim remove later — a line stored before the typed event
         carries its crossings as text. -->
    <p class="legacy">{post.text}</p>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .tests { margin: 0; padding: 0; list-style: none; }
  .tests li { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 2px 8px; padding: 5px 0; }
  .tests li + li { border-top: 1px solid var(--hair); }
  .name { font: var(--t-sm) var(--mono); color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .time { color: var(--coral); font-size: var(--t-sm); text-align: right; white-space: nowrap; }
  .meta { color: var(--muted); font-size: var(--t-cap); overflow-wrap: anywhere; }
  .runs { grid-column: 2; color: var(--faint); font-size: var(--t-cap); text-align: right; white-space: nowrap; }
  .legacy { margin: 0; color: var(--muted); font-size: var(--t-sm); white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .runs { display: none; }
  }
</style>
