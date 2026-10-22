<script>
  // The Services page: a newest-first log of agent and service events, then
  // the sessions behind them. Earlier (closed) sessions stay out of the page
  // until opened. A row opens that session's read-only transcript.
  import { untrack } from 'svelte'
  import { createGoalEvents } from '../rooms/goalEvents.ts'
  import { api } from '../../api.js'
  import { ago } from '../ui/format.js'
  import TabHeader from '../rooms/TabHeader.svelte'
  import DoingLine from '../sessions/DoingLine.svelte'
  import ServiceCards from './ServiceCards.svelte'
  import { serviceEvents, serviceSessions, when } from './events.js'
  import { buildCards } from './cards.ts'
  import { emptyData, loadServiceData } from './load.ts'
  import { applyControl } from './actions.ts'
  import { defaultFilters, filterEvents, filterSessions, rangeOptions, serviceOptions, stateOptions } from './filters.ts'

  let { roomId, members = [], changes = [], onopen } = $props()

  let journal = $state([])
  $effect(() => createGoalEvents(api, (s) => (journal = s.items)).open(roomId, ''))
  // The keeper's own check-ins stay in the entry journal; the card's clock reads them.
  // It loads with the page's data refresh, so a transient failure (a device
  // pairing mid-load) recovers on the next reload instead of staying empty.
  let checkins = $state([])
  const loadCheckins = () => { if (roomId) api.goalJournal(roomId, '', 1).then((p) => (checkins = p.items ?? []), () => {}) }

  // A room can hold thousands of sessions, so each list shows its newest
  // PAGE rows and loads the next page when its end scrolls into view.
  const PAGE = 200
  let limits = $state({ events: PAGE, current: PAGE, earlier: PAGE })
  const more = (name) => (limits[name] += PAGE)
  // loadMore calls fn when the node scrolls into view; the button also works by hand.
  function loadMore(node, fn) {
    if (typeof IntersectionObserver === 'undefined') return
    const watch = new IntersectionObserver((seen) => { if (seen[0].isIntersecting) fn() })
    watch.observe(node)
    return { destroy: () => watch.disconnect() }
  }

  // The cards read the engine's existing calls; a failed call leaves its part empty.
  let data = $state(emptyData)
  const reload = () => { loadCheckins(); loadServiceData(api, roomId, new Date(), untrack(() => data), (d) => (data = d)) }
  $effect(() => {
    reload()
    const t = setInterval(reload, 60 * 1000)
    return () => clearInterval(t)
  })
  async function change(card, control, value) {
    try {
      await applyControl(api, roomId, control.id, value)
      await reload()
      return ''
    } catch (e) {
      return String(e?.message || e)
    }
  }
  let cards = $derived(buildCards({
    now, pending: data.pending, services: data.services, reviewer: data.reviewer, queue: data.queue, history: data.history,
    changes: (changes ?? []).map((c) => ({ reviewAt: c.reviewAt, overlaps: c.overlaps?.length ?? 0 })),
    journal, checkins, settings: data.settings, usage: data.usage, voice: data.voice, members,
  }))

  let filters = $state({ ...defaultFilters })
  let allEvents = $derived(filterEvents(serviceEvents({ members, changes: changes ?? [], journal, history: data.history }), filters, now))
  let events = $derived(allEvents.slice(0, limits.events))
  let sessionLists = $derived(serviceSessions(members))
  let allCurrent = $derived(filterSessions(sessionLists.current, filters))
  let allEarlier = $derived(filterSessions(sessionLists.earlier, filters))
  let current = $derived(allCurrent.slice(0, limits.current))
  let earlier = $derived(allEarlier.slice(0, limits.earlier))
  let showEarlier = $state(false)
  let byIdMap = $derived(new Map(members.map((m) => [m.id, m])))
  const byId = (id) => byIdMap.get(id)

  // The clock ticks each second while a call runs, so its timer moves.
  let now = $state(Date.now())
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), current.some((m) => m.doing?.since) ? 1000 : 60 * 1000)
    return () => clearInterval(t)
  })
</script>

{#snippet who(name, id)}<span class="who hue-{byId(id)?.hue ?? 0}">{name}</span>{/snippet}

<section class="services" aria-label="Services">
  <TabHeader title="Services" blurb="What agents and services did, and the sessions behind it." />
  <div class="body">
    <h2>Services</h2>
    <ServiceCards {cards} {now} onchange={change} />
    <h2>Events and sessions</h2>
    <div class="filters">
      <label>Service <select bind:value={filters.service}>{#each serviceOptions as o}<option value={o.id}>{o.label}</option>{/each}</select></label>
      <label>State <select bind:value={filters.state}>{#each stateOptions as o}<option value={o.id}>{o.label}</option>{/each}</select></label>
      <label>Time <select bind:value={filters.range}>{#each rangeOptions as o}<option value={o.id}>{o.label}</option>{/each}</select></label>
    </div>
    <div class="events">
    {#each events as e (e.key)}
      {#if byId(e.session)}
        <button class="ev link" onclick={() => onopen(byId(e.session))}><time>{when(e.ts)}</time>{@render who(e.who, e.session)}<span class="what">{e.text}</span></button>
      {:else}
        <div class="ev"><time>{when(e.ts)}</time>{@render who(e.who, e.session)}<span class="what">{e.text}</span></div>
      {/if}
    {:else}
      <p class="none">No events for this filter.</p>
    {/each}
    {#if allEvents.length > events.length}<button class="more" use:loadMore={() => more('events')} onclick={() => more('events')}>Show more · {allEvents.length - events.length} left</button>{/if}
    </div>
    <h2>Sessions</h2>
    <div class="sessions">
      {#each current as m (m.id)}
        <button class="ses" onclick={() => onopen(m)}>
          <span class="dot" class:live={m.state === 'live'} aria-hidden="true"></span>{@render who(m.name, m.id)}
          <span class="what">{#if m.state === 'live' && m.doing?.text}<DoingLine doing={m.doing} {now} />{:else}{m.state === 'live' ? 'running' : m.status} · {ago(m.lastActivity)}{/if}</span>
        </button>
      {/each}
      {#if allCurrent.length > current.length}<button class="more" use:loadMore={() => more('current')} onclick={() => more('current')}>Show more · {allCurrent.length - current.length} left</button>{/if}
      {#if showEarlier}
        {#each earlier as m (m.id)}
          <button class="ses" onclick={() => onopen(m)}><span class="dot" aria-hidden="true"></span>{@render who(m.name, m.id)}<span class="what">closed · {ago(m.ended)}</span></button>
        {/each}
        {#if allEarlier.length > earlier.length}<button class="more" use:loadMore={() => more('earlier')} onclick={() => more('earlier')}>Show more · {allEarlier.length - earlier.length} left</button>{/if}
      {/if}
    </div>
    {#if allEarlier.length}
      <button class="earlier" aria-expanded={showEarlier} onclick={() => (showEarlier = !showEarlier)}>{showEarlier ? '⌄' : '›'} Earlier · {allEarlier.length} closed</button>
    {/if}
  </div>
</section>

<style>
  .services { flex: 1; min-height: 0; overflow: auto; }
  .body { padding: 0 var(--s5) var(--s5); max-width: 980px; }
  h2 { font-size: var(--t-cap); font-weight: 500; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); margin: var(--s5) 0 var(--s2); }
  .ev, .ses { display: flex; align-items: baseline; gap: var(--s3); width: 100%; padding: 7px var(--s2); border-radius: var(--r-xs); font-size: var(--t-ui); text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  .ses { align-items: center; }
  @media (hover: hover) {
    .ev.link:hover, .ses:hover { background: var(--hover); }
  }
  .ev.link:active, .ses:active { background: var(--select); }
  time { flex: none; width: 44px; font-size: var(--t-sm); color: var(--faint); font-variant-numeric: tabular-nums; }
  .who { flex: none; font-weight: 600; }
  .what { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); }
  .dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--faint); }
  .dot.live { background: var(--gold); }
  .filters { display: flex; flex-wrap: wrap; gap: var(--s4); margin-bottom: var(--s2); font-size: var(--t-ui); color: var(--muted); }
  .filters select { background: var(--field); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-xs); padding: 4px 8px; font: inherit; }
  .none { margin: var(--s2); color: var(--faint); font-size: var(--t-ui); }
  .more { display: block; width: 100%; padding: var(--s2); font-size: var(--t-ui); color: var(--muted); text-align: left; }
  .earlier { margin-top: var(--s3); padding: var(--s2); font-size: var(--t-ui); color: var(--muted); }
  @media (max-width: 760px) {
    .body { padding: 0 var(--s3) var(--s4); }
    .ev { gap: var(--s2); }
    .ev .what { white-space: normal; }
  }
</style>
