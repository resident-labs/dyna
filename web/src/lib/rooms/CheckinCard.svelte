<script lang="ts">
  // The card for a parent check-in: how many children are in each state, then
  // the stalest first, so a parent reads the child most likely to be stuck
  // before the rest. A phone shows the counts and the three stalest children.
  // It reads the typed payload of the post, never its text.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { checkinEvent } from './eventPayloads.ts'
  import type { CheckinState } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, posts = [post], notice = post.notice, surface = 'feed', focus = '' }: { post: EventPost; posts?: EventPost[]; notice?: EventNotice; surface?: EventSurface; focus?: string } = $props()
  let history = $derived(posts.slice(0, -1).reverse())

  const stateWords: Record<CheckinState, string> = { running: 'running', queued: 'queued', idle: 'idle' }
  const stateTone: Record<CheckinState, string> = { running: 'sage', queued: 'gold', idle: 'coral' }

  let payload = $derived(checkinEvent(post))
  let children = $derived(payload?.children ?? [])
  // A count of zero says nothing a parent acts on, so it stays out.
  let counts = $derived.by(() => {
    const counts = payload?.counts
    if (!counts) return []
    return (['running', 'queued', 'idle'] as const)
      .map((state) => ({ state, count: counts[state] }))
      .filter((entry) => entry.count > 0)
  })
  let title = $derived.by(() => {
    const total = children.length
    return total === 1 ? '1 child' : `${total} children`
  })
  // The card names the service that looked, never a sender.
  const producers: Record<string, string> = { 'parent-checkin': 'Parent check-in' }
  let producer = $derived(notice?.producer ? producers[notice.producer] ?? '' : '')
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  open={focus ? true : undefined}
  title={producer || 'Check-in'}
  chips={counts.map((entry) => ({ label: `${entry.count} ${stateWords[entry.state]}`, tone: stateTone[entry.state] }))}
  summary={[title, ...children.slice(0, 3).map((child) => child.name)].filter(Boolean).join(' · ')}
>
  {#snippet icon()}⌥{/snippet}
  {#snippet detail()}
  {#if children.length}
    <ul class="children">
      {#each children as child, i (i)}
        <li>
          <span class="name">{child.name}</span>
          <span class="state {stateTone[child.state]}">{stateWords[child.state]}</span>
          <span class="age">{child.age}</span>
          <span class="work" title={child.goal}>{child.goal}</span>
          <span class="pr">{child.pr}</span>
        </li>
      {/each}
    </ul>
  {:else}
    <!-- TODO: legacy/shim remove later — a check-in stored before the typed
         events shows its child table as text. Remove when that history ages out. -->
    <p class="legacy">{post.text?.split('\n').slice(1).join('\n')}</p>
  {/if}
  {#if history.length}
    <details class="history" open={history.some((item) => item.id === focus)}>
      <summary>Earlier check-ins ({history.length})</summary>
      {#each history as item (item.id)}
        {@const earlier = checkinEvent(item)}
        <details open={item.id === focus} data-id={item.id}>
          <summary>{new Date(item.ts ?? 0).toLocaleString()}</summary>
          <ul class="children">
            {#each earlier?.children ?? [] as child, i (i)}
              <li>
                <span class="name">{child.name}</span>
                <span class="state {stateTone[child.state]}">{stateWords[child.state]}</span>
                <span class="age">{child.age}</span>
                <span class="work" title={child.goal}>{child.goal}</span>
                <span class="pr">{child.pr}</span>
              </li>
            {/each}
          </ul>
        </details>
      {/each}
    </details>
  {/if}
  {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  {/snippet}
</EventRow>

<style>
  .history { margin-top: var(--s2); }
  .history summary { cursor: pointer; color: var(--muted); }
  .children { margin: 0; padding: 0; list-style: none; }
  .children li {
    display: grid;
    grid-template-columns: minmax(0, auto) auto auto minmax(0, 1fr);
    align-items: baseline;
    gap: 4px 8px;
    padding: 4px 0;
    font-size: var(--t-sm);
  }
  .children li + li { border-top: 1px solid var(--hair); }
  .name { color: var(--ink-2); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .state { font-size: var(--t-cap); font-weight: 600; }
  .state.sage { color: var(--sage); }
  .state.gold { color: var(--gold); }
  .state.coral { color: var(--coral); }
  .age { color: var(--faint); font-size: var(--t-cap); white-space: nowrap; }
  .work { color: var(--muted); overflow-wrap: anywhere; }
  .pr { grid-column: 1 / -1; color: var(--faint); font-size: var(--t-cap); overflow-wrap: anywhere; }
  .legacy { margin: 0; color: var(--muted); font-size: var(--t-sm); white-space: pre-wrap; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    /* A phone shows the counts and the three stalest children. */
    .children li { grid-template-columns: minmax(0, 1fr) auto auto; }
    .work { grid-column: 1 / -1; white-space: normal; }
  }
</style>
